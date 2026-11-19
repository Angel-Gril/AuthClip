package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这些用例对应上线前审查提出的三条阻断项：
//  1. 改派接收人后，旧数据仍留在任务里（新接收人可读）
//  2. 服务端不校验 OAuth 回传的 state 是否属于当前任务
//  3. 长期令牌可从 URL 查询参数传入（会落进访问日志/浏览器历史）

const testLink = "https://claude.ai/oauth/authorize?client_id=PLACEHOLDER&code=true" +
	"&code_challenge=CHAL&code_challenge_method=S256" +
	"&redirect_uri=http%3A%2F%2Flocalhost%3A54545%2Fcallback&response_type=code" +
	"&scope=user%3Aprofile&state=STATE-ONE"

type env struct {
	app   *app
	srv   *httptest.Server
	admin *User
	b1    *User
	b2    *User
}

func newEnv(t *testing.T, secret string) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := openStore(dir, secret)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	a := &app{st: st}
	admin, err := st.addUser("admin", "all", "A")
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := st.addUser("member", "", "B1")
	b2, _ := st.addUser("member", "", "B2")

	h, err := a.routes()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &env{app: a, srv: srv, admin: admin, b1: b1, b2: b2}
}

func (e *env) call(t *testing.T, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, e.srv.URL+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("X-Token", token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (e *env) newSlot(t *testing.T, member *User) string {
	t.Helper()
	code, out := e.call(t, "POST", "/api/slots", e.admin.Token, map[string]any{
		"member": member.ID, "link": testLink, "label": "t",
	})
	if code != http.StatusOK {
		t.Fatalf("create slot: %d %v", code, out)
	}
	return out["id"].(string)
}

func TestReassignClearsReturnedData(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)

	cb := "http://localhost:54545/callback?code=SECRET-CODE&state=STATE-ONE"
	if code, out := e.call(t, "POST", "/api/slots/"+id+"/report", e.b1.Token, map[string]any{
		"email": "b1@example.com", "callback": cb, "reg_ip": "1.2.3.4", "reg_region": "US", "node_ip": "5.6.7.8",
	}); code != http.StatusOK {
		t.Fatalf("report: %d %v", code, out)
	}

	// 改派给 B2
	if code, out := e.call(t, "PATCH", "/api/slots/"+id, e.admin.Token, map[string]any{"member": e.b2.ID}); code != http.StatusOK {
		t.Fatalf("reassign: %d %v", code, out)
	}

	// B2 打开自己的收件箱：必须看不到 B1 的任何数据
	_, mine := e.call(t, "GET", "/api/mine", e.b2.Token, nil)
	slots, _ := mine["slots"].([]any)
	if len(slots) != 1 {
		t.Fatalf("B2 应看到 1 条任务，实际 %d", len(slots))
	}
	s := slots[0].(map[string]any)
	for _, k := range []string{"email", "code", "callback", "reg_ip", "reg_region", "node_ip", "seen_ip", "state_param"} {
		if v, _ := s[k].(string); v != "" {
			t.Fatalf("改派后字段 %s 仍残留：%q", k, v)
		}
	}
}

func TestReportAfterReassignRejected(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	if _, out := e.call(t, "PATCH", "/api/slots/"+id, e.admin.Token, map[string]any{"member": e.b2.ID}); out == nil {
		t.Fatal("reassign failed")
	}
	code, _ := e.call(t, "POST", "/api/slots/"+id+"/report", e.b1.Token, map[string]any{
		"email": "b1@example.com", "callback": "http://localhost:54545/callback?code=X&state=STATE-ONE",
	})
	if code != http.StatusNotFound {
		t.Fatalf("改派后旧接收人仍能回传：%d", code)
	}
}

func TestReportRejectsForeignState(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	base := map[string]any{"email": "b1@example.com", "reg_ip": "", "reg_region": "", "node_ip": ""}

	with := func(callback string) (int, map[string]any) {
		body := map[string]any{}
		for k, v := range base {
			body[k] = v
		}
		body["callback"] = callback
		return e.call(t, "POST", "/api/slots/"+id+"/report", e.b1.Token, body)
	}

	if code, _ := with("http://localhost:54545/callback?code=OTHER&state=STATE-TWO"); code != http.StatusConflict {
		t.Fatalf("state 不一致应 409，实际 %d", code)
	}
	if code, _ := with("http://localhost:54545/callback?code=NO-STATE"); code != http.StatusBadRequest {
		t.Fatalf("缺 state 应 400，实际 %d", code)
	}
	if code, out := with("http://localhost:54545/callback?code=GOOD&state=STATE-ONE"); code != http.StatusOK {
		t.Fatalf("state 一致应通过，实际 %d %v", code, out)
	}
}

func TestTokenFromQueryRejected(t *testing.T) {
	e := newEnv(t, "")
	// 只给 ?t=，不给请求头：必须 401（历史实现会放行）
	res, err := http.Get(e.srv.URL + "/api/me?t=" + e.admin.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("查询参数令牌被接受：%d", res.StatusCode)
	}
	// 用请求头则应通过
	if code, _ := e.call(t, "GET", "/api/me", e.admin.Token, nil); code != http.StatusOK {
		t.Fatalf("请求头令牌应通过，实际 %d", code)
	}
}

func TestStoreEncryptionAtRest(t *testing.T) {
	dir := t.TempDir()
	const secret = "correct horse battery staple"
	st, err := openStore(dir, secret)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.addUser("admin", "all", "A")
	if err != nil {
		t.Fatal(err)
	}

	blob, err := os.ReadFile(filepath.Join(dir, "state.bin"))
	if err != nil {
		t.Fatalf("应生成 state.bin: %v", err)
	}
	if strings.Contains(string(blob), u.Token) || strings.Contains(string(blob), `"users"`) {
		t.Fatal("state.bin 里出现了明文令牌/字段名")
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); !os.IsNotExist(err) {
		t.Fatal("不应同时存在明文 state.json")
	}

	again, err := openStore(dir, secret)
	if err != nil {
		t.Fatalf("同口令应能解密: %v", err)
	}
	if len(again.users()) != 1 || again.users()[0].Token != u.Token {
		t.Fatal("解密后数据不一致")
	}

	if _, err := openStore(dir, "wrong secret"); err == nil {
		t.Fatal("错误口令应解密失败")
	}
}

func TestLinkStateParsing(t *testing.T) {
	if got := linkStateOf(testLink); got != "STATE-ONE" {
		t.Fatalf("linkStateOf = %q", got)
	}
	if got := linkStateOf("https://example.com/plain"); got != "" {
		t.Fatalf("无 state 的链接应返回空，实际 %q", got)
	}
	// 授权链接本身带 code=true，不能被当成回调
	if _, _, ok := parseCallback(testLink); ok {
		t.Fatal("授权链接被误判为回调链接")
	}
}

// ---------------------------------------------------------------- 权限与账号

func TestOwnScopeAdminIsIsolated(t *testing.T) {
	e := newEnv(t, "")
	// 另一个「限自己」的管理员
	own, err := e.app.st.addUser("admin", "own", "A2")
	if err != nil {
		t.Fatal(err)
	}
	// 全量管理员下发一条任务，挂在他的名下
	code, out := e.call(t, "POST", "/api/slots", e.admin.Token, map[string]any{
		"member": e.b1.ID, "link": testLink, "label": "by-A",
	})
	if code != http.StatusOK {
		t.Fatalf("create: %d %v", code, out)
	}
	id := out["id"].(string)

	// A2 的看板：看不到这条任务
	code, board := e.call(t, "GET", "/api/board", own.Token, nil)
	if code != http.StatusOK {
		t.Fatalf("board: %d", code)
	}
	if slots := asSlice(board["slots"]); len(slots) != 0 {
		t.Fatalf("限自己的管理员看到了别人的任务：%d 条", len(slots))
	}
	// 也拿不到接收人的长期链接（token）
	for _, m := range board["members"].([]any) {
		if _, hasToken := m.(map[string]any)["token"]; hasToken {
			t.Fatal("限自己的管理员拿到了接收人 token")
		}
	}
	// 账号与审计接口：403
	if code, _ := e.call(t, "GET", "/api/users", own.Token, nil); code != http.StatusForbidden {
		t.Fatalf("限自己的管理员能列账号：%d", code)
	}
	if code, _ := e.call(t, "GET", "/api/audit", own.Token, nil); code != http.StatusForbidden {
		t.Fatalf("限自己的管理员能看审计：%d", code)
	}
	// 动别人的任务：404
	for _, tc := range []struct{ method, path string }{
		{"PATCH", "/api/slots/" + id},
		{"POST", "/api/slots/" + id + "/clear"},
		{"DELETE", "/api/slots/" + id},
	} {
		if c, _ := e.call(t, tc.method, tc.path, own.Token, map[string]any{"label": "x"}); c != http.StatusNotFound {
			t.Fatalf("%s %s 应 404，实际 %d", tc.method, tc.path, c)
		}
	}
	// 自己下发的任务则能管
	code, out = e.call(t, "POST", "/api/slots", own.Token, map[string]any{
		"member": e.b1.ID, "link": testLink, "label": "by-A2",
	})
	if code != http.StatusOK {
		t.Fatalf("own 管理员无法下发：%d %v", code, out)
	}
	if c, _ := e.call(t, "PATCH", "/api/slots/"+out["id"].(string), own.Token, map[string]any{"label": "y"}); c != http.StatusOK {
		t.Fatalf("own 管理员无法改自己的任务：%d", c)
	}
}

func TestRevokedLinkStopsWorking(t *testing.T) {
	e := newEnv(t, "")
	if code, _ := e.call(t, "GET", "/api/mine", e.b1.Token, nil); code != http.StatusOK {
		t.Fatalf("吊销前应可用：%d", code)
	}
	if code, out := e.call(t, "PATCH", "/api/users/"+e.b1.ID, e.admin.Token, map[string]any{"revoke": true}); code != http.StatusOK {
		t.Fatalf("吊销失败：%d %v", code, out)
	}
	if code, _ := e.call(t, "GET", "/api/mine", e.b1.Token, nil); code != http.StatusUnauthorized {
		t.Fatalf("吊销后应 401，实际 %d", code)
	}
	// 恢复后又能用
	if code, _ := e.call(t, "PATCH", "/api/users/"+e.b1.ID, e.admin.Token, map[string]any{"revoke": false}); code != http.StatusOK {
		t.Fatal("恢复失败")
	}
	if code, _ := e.call(t, "GET", "/api/mine", e.b1.Token, nil); code != http.StatusOK {
		t.Fatalf("恢复后应 200，实际 %d", code)
	}
}

func TestRotateInvalidatesOldToken(t *testing.T) {
	e := newEnv(t, "")
	code, out := e.call(t, "POST", "/api/users/"+e.b1.ID+"/rotate", e.admin.Token, map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("轮换失败：%d %v", code, out)
	}
	if code, _ := e.call(t, "GET", "/api/mine", e.b1.Token, nil); code != http.StatusUnauthorized {
		t.Fatalf("旧链接应失效，实际 %d", code)
	}
	newTok := out["token"].(string)
	if code, _ := e.call(t, "GET", "/api/mine", newTok, nil); code != http.StatusOK {
		t.Fatalf("新链接应可用，实际 %d", code)
	}
}

func TestSelfProtection(t *testing.T) {
	e := newEnv(t, "")
	if code, _ := e.call(t, "PATCH", "/api/users/"+e.admin.ID, e.admin.Token, map[string]any{"revoke": true}); code != http.StatusBadRequest {
		t.Fatalf("应禁止吊销自己，实际 %d", code)
	}
	if code, _ := e.call(t, "DELETE", "/api/users/"+e.admin.ID, e.admin.Token, nil); code != http.StatusBadRequest {
		t.Fatalf("应禁止删除自己，实际 %d", code)
	}
}

func TestAuditTrail(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	if _, out := e.call(t, "PATCH", "/api/slots/"+id, e.admin.Token, map[string]any{"member": e.b2.ID}); out == nil {
		t.Fatal("reassign failed")
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/audit", nil)
	req.Header.Set("X-Token", e.admin.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("audit: %d", res.StatusCode)
	}
	var rows []map[string]any
	if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
		t.Fatalf("audit decode: %v", err)
	}
	var actions []string
	for _, r := range rows {
		actions = append(actions, r["action"].(string))
	}
	joined := strings.Join(actions, ",")
	if !strings.Contains(joined, "slot.create") || !strings.Contains(joined, "slot.reassign") {
		t.Fatalf("审计缺少动作：%s", joined)
	}
}


func TestArchiveKeepsMemberHistory(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	cb := "http://localhost:54545/callback?code=ARCH1&state=STATE-ONE"
	if code, _ := e.call(t, "POST", "/api/slots/"+id+"/report", e.b1.Token, map[string]any{"email": "b1@example.com", "callback": cb}); code != http.StatusOK {
		t.Fatalf("report: %d", code)
	}
	// 管理员归档
	if code, _ := e.call(t, "DELETE", "/api/slots/"+id, e.admin.Token, nil); code != http.StatusOK {
		t.Fatalf("archive: %d", code)
	}
	// 看板不再显示
	if _, board := e.call(t, "GET", "/api/board", e.admin.Token, nil); len(asSlice(board["slots"])) != 0 {
		t.Fatal("归档后看板仍显示")
	}
	// B 的历史里仍在，且带 deleted 标记
	_, mine := e.call(t, "GET", "/api/mine", e.b1.Token, nil)
	slots := asSlice(mine["slots"])
	if len(slots) != 1 {
		t.Fatalf("B 的历史应有 1 条，实际 %d", len(slots))
	}
	s := slots[0].(map[string]any)
	if s["deleted"] != true || s["code"] != "ARCH1" {
		t.Fatalf("历史内容不对：%v", s)
	}
	// 归档后不能再回传
	if code, _ := e.call(t, "POST", "/api/slots/"+id+"/report", e.b1.Token, map[string]any{"email": "b1@example.com", "callback": cb}); code == http.StatusOK {
		t.Fatal("归档任务仍可回传")
	}
}

func TestPurgeOnlyForAllScope(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	e.call(t, "DELETE", "/api/slots/"+id, e.admin.Token, nil)

	own, _ := e.app.st.addUser("admin", "own", "A2")
	// 限自己的管理员不能彻底删除
	if code, _ := e.call(t, "DELETE", "/api/slots/"+id+"?purge=1", own.Token, nil); code != http.StatusNotFound {
		t.Fatalf("own 访问他人任务应 404，实际 %d", code)
	}
	// 全量可以
	if code, _ := e.call(t, "DELETE", "/api/slots/"+id+"?purge=1", e.admin.Token, nil); code != http.StatusOK {
		t.Fatalf("purge: %d", code)
	}
	_, mine := e.call(t, "GET", "/api/mine", e.b1.Token, nil)
	if len(asSlice(mine["slots"])) != 0 {
		t.Fatal("彻底删除后 B 的历史仍可见")
	}
}

func TestOwnScopeCannotCreateMember(t *testing.T) {
	e := newEnv(t, "")
	own, _ := e.app.st.addUser("admin", "own", "A2")
	if code, _ := e.call(t, "POST", "/api/members", own.Token, map[string]any{"label": "B3"}); code != http.StatusForbidden {
		t.Fatalf("限自己的管理员能建接收人：%d", code)
	}
	if code, _ := e.call(t, "POST", "/api/members", e.admin.Token, map[string]any{"label": "B3"}); code != http.StatusOK {
		t.Fatalf("全量管理员建接收人失败：%d", code)
	}
}

func TestExportCSV(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	e.call(t, "POST", "/api/slots/"+id+"/report", e.b1.Token, map[string]any{
		"email": "b1@example.com", "callback": "http://localhost:54545/callback?code=EXP1&state=STATE-ONE",
	})
	e.call(t, "DELETE", "/api/slots/"+id, e.admin.Token, nil) // 归档的也要出现在导出里

	req, _ := http.NewRequest("GET", e.srv.URL+"/api/export?format=csv", nil)
	req.Header.Set("X-Token", e.admin.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("export: %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "csv") {
		t.Fatalf("content-type: %s", ct)
	}
	raw, _ := io.ReadAll(res.Body)
	body := string(raw)
	for _, want := range []string{"EXP1", "b1@example.com", "已归档"} {
		if !strings.Contains(body, want) {
			t.Fatalf("导出缺少 %q：\n%s", want, body)
		}
	}
	// 限自己的管理员不能导出
	own, _ := e.app.st.addUser("admin", "own", "A2")
	if code, _ := e.call(t, "GET", "/api/export?format=csv", own.Token, nil); code != http.StatusForbidden {
		t.Fatalf("own 能导出：%d", code)
	}
}


// asSlice：JSON 里 null 与 [] 都安全处理
func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

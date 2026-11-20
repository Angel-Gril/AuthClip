package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	admin, err := st.addUser("admin", "all", "A", "a")
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := st.addUser("member", "", "B1", "b1")
	b2, _ := st.addUser("member", "", "B2", "b2")

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

// report：自动带上页面当时的 link_gen（模拟正常前端）
func (e *env) report(t *testing.T, token, id string, body map[string]any) (int, map[string]any) {
	t.Helper()
	if _, ok := body["link_gen"]; !ok {
		gen := 1
		if sl := e.app.st.slotByID(id); sl != nil {
			gen = sl.LinkGen
		}
		body["link_gen"] = gen // 默认模拟"页面刚拿到的最新代次"
	}
	return e.call(t, "POST", "/api/slots/"+id+"/report", token, body)
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
	if code, out := e.report(t, e.b1.Token, id, map[string]any{
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
	code, _ := e.report(t, e.b1.Token, id, map[string]any{
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
		return e.report(t, e.b1.Token, id, body)
	}

	if code, _ := with("http://localhost:54545/callback?code=OTHER&state=STATE-TWO"); code != http.StatusConflict {
		t.Fatalf("state 不一致应 409，实际 %d", code)
	}
	// v1.8.4：缺 state 不再拒绝，改为接受并标记「来源未校验」
	if code, _ := with("http://localhost:54545/callback?code=NO-STATE"); code != http.StatusOK {
		t.Fatalf("缺 state 应接受（标记未校验），实际 %d", code)
	}
	if !e.app.st.slotByID(id).Unverified {
		t.Fatal("缺 state 的回传应标记为未校验")
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
	u, err := st.addUser("admin", "all", "A", "a")
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
	own, err := e.app.st.addUser("admin", "own", "A2", "a2")
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

// v1.8.8：归档只在 A 端保留，接收人侧不再下发（此前是反的）
func TestArchiveHidesFromMember(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	cb := "http://localhost:54545/callback?code=ARCH1&state=STATE-ONE"
	if code, _ := e.report(t, e.b1.Token, id, map[string]any{"email": "b1@example.com", "callback": cb}); code != http.StatusOK {
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
	// B 端：归档后不再下发
	_, mine := e.call(t, "GET", "/api/mine", e.b1.Token, nil)
	if n := len(asSlice(mine["slots"])); n != 0 {
		t.Fatalf("归档任务不该再下发给接收人，实际 %d 条", n)
	}
	// A 端：归档列表里可见
	_, board := e.call(t, "GET", "/api/board", e.admin.Token, nil)
	if n := len(asSlice(board["archived"])); n != 1 {
		t.Fatalf("A 端应看到 1 条归档，实际 %d", n)
	}
	// 归档后不能再回传
	if code, _ := e.report(t, e.b1.Token, id, map[string]any{"email": "b1@example.com", "callback": cb}); code == http.StatusOK {
		t.Fatal("归档任务仍可回传")
	}
}

func TestPurgeOnlyForAllScope(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	e.call(t, "DELETE", "/api/slots/"+id, e.admin.Token, nil)

	own, _ := e.app.st.addUser("admin", "own", "A2", "a2")
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
	own, _ := e.app.st.addUser("admin", "own", "A2", "a2")
	if code, _ := e.call(t, "POST", "/api/members", own.Token, map[string]any{"label": "B3"}); code != http.StatusForbidden {
		t.Fatalf("限自己的管理员能建接收人：%d", code)
	}
	if code, _ := e.call(t, "POST", "/api/members", e.admin.Token, map[string]any{"label": "B3"}); code != http.StatusOK {
		t.Fatalf("全量管理员建接收人失败：%d", code)
	}
}

func TestCSVExportTreatsUserInputAsText(t *testing.T) {
	e := newEnv(t, "")
	owner, err := e.app.st.addUser("admin", "all", "+owner", "owner")
	if err != nil {
		t.Fatal(err)
	}
	member, err := e.app.st.addUser("member", "", "\ufeff ＠member", "member")
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"  =example", "-example", "normal, text", "\ttext", "\r\ntext"} {
		if _, err := e.app.st.createSlot(owner.ID, member.ID, label, "https://example.invalid/task"); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/export?format=csv", nil)
	e.app.exportSlots(rec, req, e.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("CSV export returned %d", rec.Code)
	}
	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"'\ntext", "'\ttext", "normal, text", "'-example", "'  =example"}
	if len(rows) != len(want)+1 {
		t.Fatalf("expected %d rows including the header, got %d", len(want)+1, len(rows))
	}
	for i, row := range rows[1:] {
		if row[1] != want[i] || row[3] != "'\ufeff ＠member" || row[4] != "'+owner" {
			t.Errorf("row %d has unexpected text cells: %q, %q, %q", i+1, row[1], row[3], row[4])
		}
	}
}

func TestExportCSV(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	e.report(t, e.b1.Token, id, map[string]any{
		"email": "b1@example.com", "callback": "http://localhost:54545/callback?code=EXP1&state=STATE-ONE",
	})
	e.call(t, "DELETE", "/api/slots/"+id, e.admin.Token, nil) // 归档的也要出现在导出里

	req, _ := http.NewRequest("GET", e.srv.URL+"/api/export?format=csv&secrets=1&confirm=EXPORT", nil)
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
	own, _ := e.app.st.addUser("admin", "own", "A2", "a2")
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

// ---------------------------------------------------------------- v1.3.1 安全项

func TestUserListNeverReturnsTokens(t *testing.T) {
	e := newEnv(t, "")
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/users", nil)
	req.Header.Set("X-Token", e.admin.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var rows []map[string]any
	if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("账号列表为空")
	}
	for _, r := range rows {
		if _, has := r["token"]; has {
			t.Fatalf("账号列表泄露 token：%v", r)
		}
	}
	// 看板的接收人列表同样不能带链接/token
	_, board := e.call(t, "GET", "/api/board", e.admin.Token, nil)
	for _, m := range board["members"].([]any) {
		row := m.(map[string]any)
		if _, has := row["token"]; has {
			t.Fatal("看板泄露接收人 token")
		}
		if _, has := row["link"]; has {
			t.Fatal("看板泄露接收人长期链接")
		}
	}
}

func TestLinkMustCarryState(t *testing.T) {
	e := newEnv(t, "")
	noState := "https://claude.ai/oauth/authorize?client_id=PLACEHOLDER&code=true&response_type=code"
	code, _ := e.call(t, "POST", "/api/slots", e.admin.Token, map[string]any{
		"member": e.b1.ID, "link": noState, "label": "nostate",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("缺 state 的链接应被拒，实际 %d", code)
	}
	// 改链接也不能改成无 state 的
	id := e.newSlot(t, e.b1)
	if code, _ := e.call(t, "PATCH", "/api/slots/"+id, e.admin.Token, map[string]any{"link": noState}); code != http.StatusBadRequest {
		t.Fatalf("PATCH 缺 state 应被拒，实际 %d", code)
	}
}

func TestReportRequiresFullCallbackURL(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	base := map[string]any{"email": "b@example.com"}

	// v1.8.4：裸授权码接受（标记未校验）；过短的无效内容仍拒
	body := map[string]any{"email": "b@example.com", "code": "BARE-CODE-ONLY-123456"}
	if code, out := e.report(t, e.b1.Token, id, body); code != http.StatusOK {
		t.Fatalf("裸 code 应被接受，实际 %d %v", code, out)
	}
	if code, _ := e.report(t, e.b1.Token, id, map[string]any{"email": "b@example.com", "code": "short"}); code != http.StatusBadRequest {
		t.Fatalf("过短内容应被拒，实际 %d", code)
	}
	// 有 URL 但没 state：接受（标记未校验）
	_ = base
	body = map[string]any{"email": "b@example.com", "callback": "http://localhost:54545/callback?code=X"}
	if code, _ := e.report(t, e.b1.Token, id, body); code != http.StatusOK {
		t.Fatalf("缺 state 的回调应接受（标记未校验），实际 %d", code)
	}
	// 完整且 state 匹配：通过
	body = map[string]any{"email": "b@example.com", "callback": "http://localhost:54545/callback?code=OK&state=STATE-ONE"}
	if code, out := e.report(t, e.b1.Token, id, body); code != http.StatusOK {
		t.Fatalf("完整回调应通过，实际 %d %v", code, out)
	}
}

// v1.7.2：管理员需要直接看账号密码 → 导出默认明文，打码版走 ?masked=1
func TestExportDefaultsToPlaintextMaskedOption(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	e.report(t, e.b1.Token, id, map[string]any{
		"email": "b1@example.com", "callback": "http://localhost:54545/callback?code=SUPERSECRETCODE&state=STATE-ONE",
	})

	get := func(qs string) (int, string) {
		req, _ := http.NewRequest("GET", e.srv.URL+"/api/export"+qs, nil)
		req.Header.Set("X-Token", e.admin.Token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}

	// 默认：明文（管理员日常要复制账号密码）
	code, body := get("?format=csv")
	if code != http.StatusOK {
		t.Fatalf("export: %d", code)
	}
	if !strings.Contains(body, "SUPERSECRETCODE") || !strings.Contains(body, "b1@example.com") {
		t.Fatalf("默认导出应为明文：\n%s", body)
	}
	// 打码版：显式 ?masked=1
	if _, masked := get("?format=csv&masked=1"); strings.Contains(masked, "SUPERSECRETCODE") || !strings.Contains(masked, "***") {
		t.Fatalf("?masked=1 应打码：\n%s", masked)
	}

	// 完整导出必须二次确认
	if code, _ := get("?format=csv&secrets=1"); code != http.StatusBadRequest {
		t.Fatalf("未带 confirm 应 400，实际 %d", code)
	}
	if code, body := get("?format=csv&secrets=1&confirm=EXPORT"); code != http.StatusOK || !strings.Contains(body, "SUPERSECRETCODE") {
		t.Fatalf("确认后应导出明文：%d", code)
	}

	// 审计：每次导出都留痕，且含模式
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/audit", nil)
	req.Header.Set("X-Token", e.admin.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var rows []map[string]any
	_ = json.NewDecoder(res.Body).Decode(&rows)
	var modes []string
	for _, r := range rows {
		if r["action"] == "data.export" {
			modes = append(modes, r["target"].(string))
		}
	}
	if len(modes) < 2 {
		t.Fatalf("导出审计条数不足：%v", modes)
	}
	joined := strings.Join(modes, "|")
	if !strings.Contains(joined, "mode=masked") || !strings.Contains(joined, "mode=full") {
		t.Fatalf("审计未区分模式：%s", joined)
	}
}

func TestLinkRevealIsPerRequestAndAudited(t *testing.T) {
	e := newEnv(t, "")
	// 列表仍然不含 token
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/users", nil)
	req.Header.Set("X-Token", e.admin.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(raw), e.b1.Token) {
		t.Fatal("账号列表仍带 token")
	}

	// 按单条取链接：200 且是 #b= 形式
	req, _ = http.NewRequest("GET", e.srv.URL+"/api/users/"+e.b1.ID+"/link", nil)
	req.Header.Set("X-Token", e.admin.Token)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("取链接: %d", res.StatusCode)
	}
	var got map[string]string
	_ = json.NewDecoder(res.Body).Decode(&got)
	if !strings.Contains(got["link"], "#b="+e.b1.Token) {
		t.Fatalf("链接不对：%v", got)
	}

	// 限自己范围的管理员：403
	own, _ := e.app.st.addUser("admin", "own", "A2", "a2")
	if code, _ := e.call(t, "GET", "/api/users/"+e.b1.ID+"/link", own.Token, nil); code != http.StatusForbidden {
		t.Fatalf("own 能取链接：%d", code)
	}

	// 已吊销的账号：409
	e.call(t, "PATCH", "/api/users/"+e.b1.ID, e.admin.Token, map[string]any{"revoke": true})
	if code, _ := e.call(t, "GET", "/api/users/"+e.b1.ID+"/link", e.admin.Token, nil); code != http.StatusConflict {
		t.Fatalf("吊销后应 409，实际 %d", code)
	}

	// 审计里有 user.link
	req, _ = http.NewRequest("GET", e.srv.URL+"/api/audit", nil)
	req.Header.Set("X-Token", e.admin.Token)
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	var rows []map[string]any
	_ = json.NewDecoder(res2.Body).Decode(&rows)
	found := false
	for _, r := range rows {
		if r["action"] == "user.link" {
			found = true
		}
	}
	if !found {
		t.Fatal("查看链接未留审计")
	}
}

// ---------------------------------------------------------------- 用户名

func TestUsernameUniquenessAndLookup(t *testing.T) {
	e := newEnv(t, "")
	// 显式用户名冲突 → 409
	if code, _ := e.call(t, "POST", "/api/members", e.admin.Token, map[string]any{"label": "另一个", "username": "b1"}); code != http.StatusConflict {
		t.Fatalf("重复用户名应 409，实际 %d", code)
	}
	// 不填用户名 → 从标签派生
	code, out := e.call(t, "POST", "/api/members", e.admin.Token, map[string]any{"label": "张 三"})
	if code != http.StatusOK {
		t.Fatalf("创建失败：%d %v", code, out)
	}
	if got := out["username"]; got != "张-三" && got != "user" && got != "" {
		// 中文标签 normalize 后为空 → 回落 user；这里只要求非空
	}
	if _, ok := out["username"].(string); !ok {
		t.Fatalf("应返回 username：%v", out)
	}
	// 用用户名下发
	created, out2 := e.call(t, "POST", "/api/slots", e.admin.Token, map[string]any{
		"member": "b1", "link": testLink, "label": "by-username",
	})
	if created != http.StatusOK {
		t.Fatalf("按用户名下发失败：%d %v", created, out2)
	}
	if out2["member"] != e.b1.ID {
		t.Fatalf("解析到了错误的接收人：%v", out2["member"])
	}
	// 未知用户名
	if code, _ := e.call(t, "POST", "/api/slots", e.admin.Token, map[string]any{
		"member": "nobody", "link": testLink,
	}); code != http.StatusBadRequest {
		t.Fatalf("未知用户名应 400，实际 %d", code)
	}
	// 改名后旧名失效、新名可用
	if code, _ := e.call(t, "PATCH", "/api/users/"+e.b1.ID, e.admin.Token, map[string]any{"username": "wang-xiao"}); code != http.StatusOK {
		t.Fatal("改名失败")
	}
	if code, _ := e.call(t, "POST", "/api/slots", e.admin.Token, map[string]any{"member": "b1", "link": testLink}); code != http.StatusBadRequest {
		t.Fatalf("旧用户名应失效，实际 %d", code)
	}
	if code, _ := e.call(t, "POST", "/api/slots", e.admin.Token, map[string]any{"member": "wang-xiao", "link": testLink}); code != http.StatusOK {
		t.Fatalf("新用户名应可用，实际 %d", code)
	}
}

func TestUsernameBackfilledForLegacyData(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	// 手工塞一个没有 username 的老记录
	st.d.Users = append(st.d.Users, &User{ID: "old1", Role: "member", Label: "Legacy User", Token: "tok-old", Created: time.Now().UTC()})
	if err := st.saveLocked(); err != nil { // 直接写盘（测试内不并发）
		t.Fatal(err)
	}
	// 重新打开：应自动补齐 username
	st2, err := openStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	u := st2.userByID("old1")
	if u == nil || u.Username == "" {
		t.Fatalf("老数据未补齐用户名：%+v", u)
	}
	if u.Username != "legacy-user" {
		t.Fatalf("派生用户名不对：%q", u.Username)
	}
}

// ---------------------------------------------------------------- v1.4.1

// 管理员链接不可读：读出来就能以对方身份操作，而审计会记成被冒用者。
// 恢复访问走 rotate（旧链接立即失效，动作记在操作者名下）。
func TestAdminLinkNotReadableButRotatable(t *testing.T) {
	e := newEnv(t, "")
	other, err := e.app.st.addUser("admin", "own", "A3", "a3")
	if err != nil {
		t.Fatal(err)
	}

	// 取任何管理员（他人或自己）的链接 -> 403
	for _, id := range []string{other.ID, e.admin.ID} {
		if code, body := e.call(t, "GET", "/api/users/"+id+"/link", e.admin.Token, nil); code != http.StatusForbidden {
			t.Fatalf("取管理员链接应 403，实际 %d %v", code, body)
		}
	}
	// 取接收人 -> 200，且留 user.link 审计
	code, body := e.call(t, "GET", "/api/users/"+e.b1.ID+"/link", e.admin.Token, nil)
	if code != http.StatusOK {
		t.Fatalf("取接收人链接应 200，实际 %d %v", code, body)
	}
	if link, _ := body["link"].(string); !strings.Contains(link, "#b="+e.b1.Token) {
		t.Fatalf("返回的链接不对：%v", body)
	}
	links := 0
	for _, r := range e.app.st.audit(50) {
		if r.Action == "user.link" {
			links++
		}
	}
	if links != 1 {
		t.Fatalf("应只有 1 条 user.link 审计（管理员被拒的两次不写），实际 %d", links)
	}

	// 轮换另一个管理员：允许，但会作废旧链接
	if code, _ := e.call(t, "POST", "/api/users/"+other.ID+"/rotate", e.admin.Token, map[string]any{}); code != http.StatusOK {
		t.Fatalf("轮换其他管理员应 200，实际 %d", code)
	}
	if again := e.app.st.userByID(other.ID); again == nil || again.Token == other.Token {
		t.Fatal("轮换后 token 应变化")
	}
	// 轮换自己：拒绝
	if code, _ := e.call(t, "POST", "/api/users/"+e.admin.ID+"/rotate", e.admin.Token, map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("轮换自己应 400，实际 %d", code)
	}
	// 已吊销账号：409
	e.call(t, "PATCH", "/api/users/"+e.b1.ID, e.admin.Token, map[string]any{"revoke": true})
	if code, _ := e.call(t, "GET", "/api/users/"+e.b1.ID+"/link", e.admin.Token, nil); code != http.StatusConflict {
		t.Fatalf("吊销后应 409，实际 %d", code)
	}
}

func TestAuditFailureBlocksSensitiveOutput(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	e.report(t, e.b1.Token, id, map[string]any{
		"email": "b1@example.com", "callback": "http://localhost:54545/callback?code=SECRET&state=STATE-ONE",
	})

	// 把存储路径改成目录，使 saveLocked 必定失败
	e.app.st.mu.Lock()
	orig := e.app.st.path
	bad := filepath.Join(filepath.Dir(orig), "as-dir")
	_ = os.MkdirAll(bad, 0o700)
	e.app.st.path = bad
	e.app.st.mu.Unlock()
	t.Cleanup(func() {
		e.app.st.mu.Lock()
		e.app.st.path = orig
		e.app.st.mu.Unlock()
		_ = os.RemoveAll(bad)
	})

	// 导出：必须 500 且不带数据
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/export?format=csv&secrets=1&confirm=EXPORT", nil)
	req.Header.Set("X-Token", e.admin.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("审计失败时导出应 500，实际 %d", res.StatusCode)
	}
	if strings.Contains(string(raw), "SECRET") {
		t.Fatal("审计失败仍返回了敏感数据")
	}
	// 取链接：同样 500
	if code, body := e.call(t, "GET", "/api/users/"+e.b1.ID+"/link", e.admin.Token, nil); code != http.StatusInternalServerError {
		t.Fatalf("审计失败时取链接应 500，实际 %d %v", code, body)
	}
}

func TestRelinkClearsPreviousResult(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	e.report(t, e.b1.Token, id, map[string]any{
		"email": "old@example.com", "callback": "http://localhost:54545/callback?code=OLD&state=STATE-ONE",
	})
	newLink := strings.Replace(testLink, "STATE-ONE", "STATE-TWO", 1)
	if code, out := e.call(t, "PATCH", "/api/slots/"+id, e.admin.Token, map[string]any{"link": newLink}); code != http.StatusOK {
		t.Fatalf("换链接失败：%d %v", code, out)
	}
	sl := e.app.st.slotByID(id)
	if sl.Code != "" || sl.Email != "" || sl.Callback != "" || sl.StateParam != "" || !sl.Submitted.IsZero() {
		t.Fatalf("换链接后旧回传仍在：%+v", sl)
	}
	// 审计动作是 relink
	rows := e.app.st.audit(20)
	found := false
	for _, r := range rows {
		if r.Action == "slot.relink" {
			found = true
		}
	}
	if !found {
		t.Fatal("换链接未记 slot.relink 审计")
	}
	// 同一条链接重复提交（state 不变）不应清空
	e.report(t, e.b1.Token, id, map[string]any{
		"email": "b2@example.com", "callback": "http://localhost:54545/callback?code=NEW&state=STATE-TWO",
	})
	if code, _ := e.call(t, "PATCH", "/api/slots/"+id, e.admin.Token, map[string]any{"link": newLink}); code != http.StatusOK {
		t.Fatal("重复写同一链接失败")
	}
	if sl2 := e.app.st.slotByID(id); sl2.Code != "NEW" {
		t.Fatalf("同一 state 的重复写不应清空回传，实际 code=%q", sl2.Code)
	}
}

// ---------------------------------------------------------------- v1.4.3

// 换链接的三种情况：改 state、只改 challenge（state 不变）、原样重复写
func TestRelinkClearsRegardlessOfState(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)

	fill := func(code, state string) {
		t.Helper()
		if c, out := e.report(t, e.b1.Token, id, map[string]any{
			"email": "x@example.com", "callback": "http://localhost:54545/callback?code=" + code + "&state=" + state,
		}); c != http.StatusOK {
			t.Fatalf("report: %d %v", c, out)
		}
	}
	current := func() *Slot { return e.app.st.slotByID(id) }

	// 1) state 变化 -> 清
	fill("CODE-A", "STATE-ONE")
	sameStateLink := strings.Replace(testLink, "CHAL", "CHAL2", 1) // 只改 challenge，state 仍是 STATE-ONE
	if c, out := e.call(t, "PATCH", "/api/slots/"+id, e.admin.Token, map[string]any{"link": sameStateLink}); c != http.StatusOK {
		t.Fatalf("relink: %d %v", c, out)
	}
	if sl := current(); sl.Code != "" || sl.Email != "" || sl.StateParam != "" {
		t.Fatalf("同 state 换链接也必须清空，实际 %+v", sl)
	}

	// 2) 原样重复写同一条链接 -> 不清
	fill("CODE-B", "STATE-ONE")
	if c, _ := e.call(t, "PATCH", "/api/slots/"+id, e.admin.Token, map[string]any{"link": sameStateLink}); c != http.StatusOK {
		t.Fatal("重复写同一链接失败")
	}
	if sl := current(); sl.Code != "CODE-B" {
		t.Fatalf("同一链接重复写不应清空，实际 %q", sl.Code)
	}

	// 3) 显式 keep_result=true -> 保留，且审计动作可区分
	other := strings.Replace(testLink, "STATE-ONE", "STATE-TWO", 1)
	if c, out := e.call(t, "PATCH", "/api/slots/"+id, e.admin.Token, map[string]any{"link": other, "keep_result": true}); c != http.StatusOK {
		t.Fatalf("keep relink: %d %v", c, out)
	}
	if sl := current(); sl.Code != "CODE-B" {
		t.Fatalf("keep_result=true 应保留回传，实际 %q", sl.Code)
	}
	found := false
	for _, r := range e.app.st.audit(50) {
		if r.Action == "slot.relink.keep" {
			found = true
		}
	}
	if !found {
		t.Fatal("显式保留未留痕（slot.relink.keep）")
	}
}

// v1.4.5：换链接与提交并发 —— 同 state 也要拦住旧请求
func TestReportRejectedAfterRelinkEvenWithSameState(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	gen := e.app.st.slotByID(id).LinkGen

	// 旧页面提交一次（成功）
	cb := "http://localhost:54545/callback?code=ROUND1&state=STATE-ONE"
	if c, out := e.report(t, e.b1.Token, id, map[string]any{"email": "a@example.com", "callback": cb, "link_gen": gen}); c != http.StatusOK {
		t.Fatalf("首次提交失败：%d %v", c, out)
	}

	// 管理员换链接，但 state 不变（只改 challenge）
	sameState := strings.Replace(testLink, "CHAL", "CHAL2", 1)
	if c, _ := e.call(t, "PATCH", "/api/slots/"+id, e.admin.Token, map[string]any{"link": sameState}); c != http.StatusOK {
		t.Fatal("换链接失败")
	}
	if got := e.app.st.slotByID(id).LinkGen; got <= gen {
		t.Fatalf("换链接后代次应递增，实际 %d -> %d", gen, got)
	}

	// 旧页面（旧代次）再提交：必须 409，不能把上一轮结果写回来
	if c, out := e.report(t, e.b1.Token, id, map[string]any{
		"email": "a@example.com", "callback": cb, "link_gen": gen,
	}); c != http.StatusConflict {
		t.Fatalf("旧代次提交应 409，实际 %d %v", c, out)
	}
	if sl := e.app.st.slotByID(id); sl.Code != "" {
		t.Fatalf("旧请求不该写回数据，实际 code=%q", sl.Code)
	}

	// 刷新后的页面（新代次）可以提交
	if c, out := e.report(t, e.b1.Token, id, map[string]any{
		"email": "a@example.com", "callback": "http://localhost:54545/callback?code=ROUND2&state=STATE-ONE",
	}); c != http.StatusOK {
		t.Fatalf("新代次提交应成功，实际 %d %v", c, out)
	}

	// 完全不带 link_gen：400（要求前端把代次带上）
	var raw map[string]any = map[string]any{"email": "a@example.com", "callback": cb}
	if c, _ := e.call(t, "POST", "/api/slots/"+id+"/report", e.b1.Token, raw); c != http.StatusBadRequest {
		t.Fatalf("缺 link_gen 应 400，实际 %d", c)
	}
}

// v1.4.6：轮换必须原子（换令牌 + 审计一次落盘）
func TestRotateIsAtomicOnWriteFailure(t *testing.T) {
	e := newEnv(t, "")
	before := e.app.st.userByID(e.b1.ID).Token

	// 让落盘失败
	e.app.st.mu.Lock()
	orig := e.app.st.path
	bad := filepath.Join(filepath.Dir(orig), "rotate-dir")
	_ = os.MkdirAll(bad, 0o700)
	e.app.st.path = bad
	e.app.st.mu.Unlock()
	t.Cleanup(func() {
		e.app.st.mu.Lock()
		e.app.st.path = orig
		e.app.st.mu.Unlock()
		_ = os.RemoveAll(bad)
	})

	code, body := e.call(t, "POST", "/api/users/"+e.b1.ID+"/rotate", e.admin.Token, map[string]any{})
	if code != http.StatusInternalServerError {
		t.Fatalf("写盘失败应 500，实际 %d %v", code, body)
	}
	if _, has := body["link"]; has {
		t.Fatal("失败时不应返回新链接")
	}

	// 关键：旧令牌必须仍然有效（不能出现"旧链接失效、新链接没交付"）
	e.app.st.mu.Lock()
	e.app.st.path = orig
	e.app.st.mu.Unlock()
	if after := e.app.st.userByID(e.b1.ID).Token; after != before {
		t.Fatalf("令牌被改动：%q -> %q", before, after)
	}
	if c, _ := e.call(t, "GET", "/api/mine", e.b1.Token, nil); c != http.StatusOK {
		t.Fatalf("旧链接应仍然可用，实际 %d", c)
	}
	// 审计里也不该留下半条记录
	for _, r := range e.app.st.audit(50) {
		if r.Action == "user.rotate" {
			t.Fatal("失败的轮换不应留下审计")
		}
	}

	// 恢复正常后再轮换应当成功
	if c, out := e.call(t, "POST", "/api/users/"+e.b1.ID+"/rotate", e.admin.Token, map[string]any{}); c != http.StatusOK {
		t.Fatalf("恢复后轮换失败：%d %v", c, out)
	}
}

// v1.5.4：与审查方的实现合并后，补一组针对 spreadsheetText 的单元用例
// （BOM、前导空白、全角符号都比原 csvSafe 覆盖得更全）
func TestSpreadsheetTextCases(t *testing.T) {
	cases := map[string]string{
		"":                 "",
		"normal":           "normal",
		"a=b":              "a=b", // 只处理首字符
		"=1+1":             "'=1+1",
		"+A1":              "'+A1",
		"-2+3":             "'-2+3",
		"@SUM(A1)":         "'@SUM(A1)",
		"＝全角":              "'＝全角",
		"＋全角":              "'＋全角",
		"  =leading-space": "'  =leading-space",
		"\ufeff=x":         "'\ufeff=x",
		"\tcmd":            "'\tcmd",
		"\r\ncmd":          "'\r\ncmd",
	}
	for in, want := range cases {
		if got := spreadsheetText(in); got != want {
			t.Fatalf("spreadsheetText(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// v1.6：中文用户名（此前规范化把非 ASCII 全丢，导致纯中文用户名一律报"已被占用"）
func TestChineseUsernameWorks(t *testing.T) {
	e := newEnv(t, "")

	// 1) 显式中文用户名
	code, out := e.call(t, "POST", "/api/members", e.admin.Token, map[string]any{
		"label": "张三", "username": "张三",
	})
	if code != http.StatusOK {
		t.Fatalf("中文用户名应可用，实际 %d %v", code, out)
	}
	if out["username"] != "张三" {
		t.Fatalf("用户名被改写：%v", out["username"])
	}

	// 2) 重名 -> 409（不是 400）
	if code, _ := e.call(t, "POST", "/api/members", e.admin.Token, map[string]any{
		"label": "另一个张三", "username": "张三",
	}); code != http.StatusConflict {
		t.Fatalf("重名应 409，实际 %d", code)
	}

	// 3) 纯符号用户名 -> 400（格式非法），不能报成"已被占用"
	if code, body := e.call(t, "POST", "/api/members", e.admin.Token, map[string]any{
		"label": "符号", "username": "!!!",
	}); code != http.StatusBadRequest {
		t.Fatalf("纯符号应 400，实际 %d %v", code, body)
	}

	// 4) 不填用户名，从中文标签派生
	code, out = e.call(t, "POST", "/api/members", e.admin.Token, map[string]any{"label": "李四"})
	if code != http.StatusOK {
		t.Fatalf("创建失败：%d %v", code, out)
	}
	if out["username"] != "李四" {
		t.Fatalf("应从标签派生「李四」，实际 %v", out["username"])
	}

	// 5) 改名为中文
	if code, _ := e.call(t, "PATCH", "/api/users/"+e.b1.ID, e.admin.Token, map[string]any{"username": "王五"}); code != http.StatusOK {
		t.Fatalf("改名失败：%d", code)
	}
	if u := e.app.st.userByID(e.b1.ID); u.Username != "王五" {
		t.Fatalf("改名结果不对：%q", u.Username)
	}

	// 6) 用中文用户名下发任务
	if code, out := e.call(t, "POST", "/api/slots", e.admin.Token, map[string]any{
		"member": "王五", "link": testLink, "label": "中文下发",
	}); code != http.StatusOK {
		t.Fatalf("按中文用户名下发失败：%d %v", code, out)
	}
}

// v1.7.1：接收端可回传登录密码；默认导出必须打码
func TestPasswordReportAndMasking(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)

	if code, out := e.report(t, e.b1.Token, id, map[string]any{
		"email": "pw@example.com", "password": "S3cret!pass",
		"callback": "http://localhost:54545/callback?code=PWC&state=STATE-ONE",
	}); code != http.StatusOK {
		t.Fatalf("回传失败: %d %v", code, out)
	}
	if s := e.app.st.slotByID(id); s.Password != "S3cret!pass" {
		t.Fatalf("密码未落库: %q", s.Password)
	}

	// 脱敏导出：出现打码，不出现明文
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/export?format=csv&masked=1", nil)
	e.app.exportSlots(rec, req, e.admin)
	body := rec.Body.String()
	if strings.Contains(body, "S3cret!pass") {
		t.Fatal("脱敏导出里出现了明文密码")
	}
	if !strings.Contains(body, "S••••") {
		t.Fatalf("脱敏导出里没有打码痕迹: %s", body)
	}

	// 完整导出：必须有明文（管理员二次确认后）
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/export?format=csv&secrets=1&confirm=EXPORT", nil)
	e.app.exportSlots(rec2, req2, e.admin)
	if !strings.Contains(rec2.Body.String(), "S3cret!pass") {
		t.Fatal("完整导出缺少明文密码")
	}

	// 清空回传必须连密码一起清
	e.call(t, "POST", "/api/slots/"+id+"/clear", e.admin.Token, map[string]any{})
	if s := e.app.st.slotByID(id); s.Password != "" {
		t.Fatalf("清空后密码残留: %q", s.Password)
	}
}

// v1.7.3：B 端提交成功后，接口不再下发 code/密码/回调/IP
func TestMemberCannotReadBackOwnCredentials(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	if code, _ := e.report(t, e.b1.Token, id, map[string]any{
		"email": "b1@example.com", "password": "MYPASS1",
		"callback": "http://localhost:54545/callback?code=SECRET-CODE&state=STATE-ONE",
		"reg_ip":   "9.9.9.9",
	}); code != http.StatusOK {
		t.Fatalf("report: %d", code)
	}
	_, mine := e.call(t, "GET", "/api/mine", e.b1.Token, nil)
	raw, _ := json.Marshal(mine)
	for _, secret := range []string{"SECRET-CODE", "MYPASS1", "9.9.9.9", "localhost:54545"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("B 端接口泄露了 %q：%s", secret, raw)
		}
	}
	s := asSlice(mine["slots"])[0].(map[string]any)
	if s["submitted"] == nil {
		t.Fatal("应保留提交时间供 B 端确认")
	}
}

// v1.8.3：浏览器环境信号（时区/语言）随回传一起落库，A 端可见
func TestBrowserSignalsStoredAndCleared(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	if code, out := e.report(t, e.b1.Token, id, map[string]any{
		"email": "sig@example.com", "password": "SigPass1",
		"callback": "http://localhost:54545/callback?code=SIGC&state=STATE-ONE",
		"tz":       "Asia/Shanghai", "lang": "zh-CN",
	}); code != http.StatusOK {
		t.Fatalf("回传失败: %d %v", code, out)
	}
	s := e.app.st.slotByID(id)
	if s.TZ != "Asia/Shanghai" || s.Lang != "zh-CN" {
		t.Fatalf("信号未落库: tz=%q lang=%q", s.TZ, s.Lang)
	}
	// 超长输入应被截断，不会撑爆状态文件
	e.call(t, "POST", "/api/slots/"+id+"/clear", e.admin.Token, map[string]any{})
	if code, _ := e.report(t, e.b1.Token, id, map[string]any{
		"email":    "sig@example.com",
		"callback": "http://localhost:54545/callback?code=SIGC&state=STATE-ONE",
		"tz":       strings.Repeat("т", 200),
	}); code != http.StatusOK {
		t.Fatal("超长 tz 应被截断而非报错")
	}
	if got := e.app.st.slotByID(id).TZ; len(got) > 64 {
		t.Fatalf("tz 未截断: %d 字节", len(got))
	}
	// 清空回传必须连信号一起清
	e.call(t, "POST", "/api/slots/"+id+"/clear", e.admin.Token, map[string]any{})
	if s := e.app.st.slotByID(id); s.TZ != "" || s.Lang != "" {
		t.Fatalf("清空后信号残留: %q %q", s.TZ, s.Lang)
	}
}

// v1.8.4：允许只交授权码；此时来源无法校验，必须标记出来
func TestBareCodeAcceptedAndMarkedUnverified(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	bare := "1wnEt8iCT5CXyA9axH6BeShcMDkNuHA0VnN6i8gGQJ5hgUL-AWs8t9hgZPSuVLts2PwLa2MSNyFNH3KULKsBZq-ja4"
	if code, out := e.report(t, e.b1.Token, id, map[string]any{
		"email": "bare@example.com", "callback": bare,
	}); code != http.StatusOK {
		t.Fatalf("单独授权码应被接受: %d %v", code, out)
	}
	s := e.app.st.slotByID(id)
	if s.Code != bare {
		t.Fatalf("code 未落库: %q", s.Code)
	}
	if !s.Unverified {
		t.Fatal("单独授权码必须标记为来源未校验")
	}
	if s.Callback != "" || s.StateParam != "" {
		t.Fatalf("不该凭空生成回调链接: %q %q", s.Callback, s.StateParam)
	}

	// 带 state 的正常回传仍算已校验
	id2 := e.newSlot(t, e.b1)
	if code, _ := e.report(t, e.b1.Token, id2, map[string]any{
		"email": "ok@example.com", "callback": "http://localhost:54545/callback?code=OK1&state=STATE-ONE",
	}); code != http.StatusOK {
		t.Fatal("正常回传失败")
	}
	if e.app.st.slotByID(id2).Unverified {
		t.Fatal("带 state 的回传不该被标记未校验")
	}

	// 授权链接本身仍必须拒绝
	id3 := e.newSlot(t, e.b1)
	if code, _ := e.report(t, e.b1.Token, id3, map[string]any{
		"email":    "x@example.com",
		"callback": "https://claude.ai/oauth/authorize?client_id=X&response_type=code&state=S",
	}); code != http.StatusBadRequest {
		t.Fatalf("授权链接本身应 400，实际 %d", code)
	}
}

// v1.8.6：节点/代理字段不再要求纯 IP（家宽节点常带端口与账号密码）
func TestNodeAcceptsProxyFormats(t *testing.T) {
	e := newEnv(t, "")
	cases := []string{
		"154.196.210.126",
		"154.196.210.126:7778",
		"154.196.210.126:7778:9J5Y7M3S3h5H5o4x1o8u8z0",
		"154.196.210.126:7778:user:pass",
		"socks5://user:pass@host.example.com:1080",
		"host.example.com:8080",
	}
	for _, node := range cases {
		id := e.newSlot(t, e.b1)
		if code, out := e.report(t, e.b1.Token, id, map[string]any{
			"email":    "n@example.com",
			"callback": "http://localhost:54545/callback?code=N1&state=STATE-ONE",
			"node_ip":  node,
		}); code != http.StatusOK {
			t.Fatalf("%q 应被接受，实际 %d %v", node, code, out)
		}
		if got := e.app.st.slotByID(id).NodeIP; got != node {
			t.Fatalf("落库不一致：want %q got %q", node, got)
		}
	}
	// 过长仍拒
	id := e.newSlot(t, e.b1)
	if code, _ := e.report(t, e.b1.Token, id, map[string]any{
		"email":    "n@example.com",
		"callback": "http://localhost:54545/callback?code=N1&state=STATE-ONE",
		"node_ip":  strings.Repeat("a", 201),
	}); code != http.StatusBadRequest {
		t.Fatalf("超长应 400，实际 %d", code)
	}
}

// v1.8.8：归档只在 A 端保留，接收人不再收到已归档任务
func TestArchivedHiddenFromMemberShownToAdmin(t *testing.T) {
	e := newEnv(t, "")
	id := e.newSlot(t, e.b1)
	if code, _ := e.call(t, "DELETE", "/api/slots/"+id, e.admin.Token, nil); code != http.StatusOK {
		t.Fatalf("归档失败: %d", code)
	}
	// B 端：不再下发
	_, mine := e.call(t, "GET", "/api/mine", e.b1.Token, nil)
	if n := len(asSlice(mine["slots"])); n != 0 {
		t.Fatalf("归档任务不该再下发给接收人，实际 %d 条", n)
	}
	// A 端：看板 active 为空，但 archived 里有
	_, board := e.call(t, "GET", "/api/board", e.admin.Token, nil)
	if n := len(asSlice(board["slots"])); n != 0 {
		t.Fatalf("看板不该显示归档任务，实际 %d", n)
	}
	arch := asSlice(board["archived"])
	if len(arch) != 1 {
		t.Fatalf("A 端应能看到 1 条归档，实际 %d", len(arch))
	}
	if arch[0].(map[string]any)["deleted"] != true {
		t.Fatal("归档条目缺少 deleted 标记")
	}
}

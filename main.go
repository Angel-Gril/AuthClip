package main

// AuthClip v2 —— 长期链接版
//
// 形态（按实际流程）：
//   A 有一条长期链接  https://host/#a=<token>   —— 不登录，打开就是看板
//   B 有一条长期链接  https://host/#b=<token>   —— 不登录，打开就是"我的授权"
//   A 把 Claude 授权链接丢进某个 B 的收件箱；B 点开走官方 OAuth；
//   B 回来后把地址栏那条 http://localhost:54545/callback?code=…&state=… 整条粘进来，
//   页面自动切出 code / state，连同注册邮箱、注册地区、注册 IP、静态家宽节点一起回传。
//
// 红线：
//  1. 服务端对授权链接 / 回调链接【零出站请求】。link 与 callback 都只当字符串存。
//  2. 不记录管理员 IP：admin 路由从不调用 observed()，无访问日志。
//  3. token 只走请求头；只出现在 URL fragment（#a= / #b=），永不进请求行、不进 Referer、
//     不进反代日志 —— 服务端不接受 ?t= 之类的查询参数（v1.1 修）。
//  4. 改派接收人 = 清空该任务已回传的一切数据；回传时在事务内再次校验接收人（v1.1 修）。
//  5. 回传的 state 必须与当前授权链接里的 state 一致，否则拒收（v1.1 修）。
//  6. 配置 AUTHCLIP_SECRET 后，令牌与回传数据整体 AES-256-GCM 加密落盘（v1.1 加）。

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"embed"

	maxminddb "github.com/oschwald/maxminddb-golang/v2"
)

//go:embed web
var webFS embed.FS

const (
	maxBody    = 128 << 10
	maxLink    = 8192
	sessionEC  = 20 * time.Second
	appVersion = "v1.8.8" // v1.8.7: 结果提到下发下方｜v1.8.8: 归档只留 A 端（折叠），B 端不再下发
)

// ---------------------------------------------------------------- 模型

type User struct {
	ID       string    `json:"id"`
	Username string    `json:"username"` // 唯一，用于下发给谁
	Role     string    `json:"role"`     // admin | member
	Scope    string    `json:"scope"`    // admin: all | own；member 忽略
	Label    string    `json:"label"`
	Token    string    `json:"token"`
	Revoked  bool      `json:"revoked"`
	Created  time.Time `json:"created"`
}

// scopeOf：空值按 all 处理（兼容 v1.1 之前建的管理员）
func (u *User) scopeOf() string {
	if u.Role != "admin" {
		return "member"
	}
	if u.Scope == "own" {
		return "own"
	}
	return "all"
}

func (u *User) canSeeAll() bool { return u.scopeOf() == "all" }

type Slot struct {
	ID      string    `json:"id"`
	Owner   string    `json:"owner"`  // admin id
	Member  string    `json:"member"` // member id
	Label   string    `json:"label"`
	Link    string    `json:"link"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Rev     int       `json:"rev"`

	// B 回传
	Email      string    `json:"email"`
	Password   string    `json:"password"`
	Unverified bool      `json:"unverified,omitempty"` // 只交 code、没有 state：来源无法校验
	Code       string    `json:"code"`
	Callback   string    `json:"callback"`
	StateParam string    `json:"state_param"`
	RegIP      string    `json:"reg_ip"`
	RegRegion  string    `json:"reg_region"`
	NodeIP     string    `json:"node_ip"`
	Submitted  time.Time `json:"submitted"`

	// 出口 IP 画像（提交时按观测 IP 向 ip.net.coffee 查询后落库）
	Risk *IPRisk `json:"risk,omitempty"`

	// 浏览器环境信号（B 提交时自带，无需填写）：用于对照 IP 归属地
	TZ   string `json:"tz,omitempty"`   // 例：Asia/Shanghai
	Lang string `json:"lang,omitempty"` // 例：zh-CN

	// 服务端观测（仅 member 提交时写入）
	SeenIP     string `json:"seen_ip"`
	SeenRegion string `json:"seen_region"`

	// 归档：A 端删除后从看板消失，但 B 仍能在自己的历史里看到
	Deleted   bool      `json:"deleted"`
	DeletedAt time.Time `json:"deleted_at"`

	// LinkGen：换链接就 +1。回传必须带上它，防止"清空之后旧请求又写回来"。
	// 只比 state 不够：新旧链接可能沿用同一个 state。
	LinkGen int `json:"link_gen"`
}

func (s *Slot) filled() bool { return s.Code != "" || s.Email != "" }

type data struct {
	Users []*User  `json:"users"`
	Slots []*Slot  `json:"slots"`
	Audit []*Audit `json:"audit"`
}

// Audit 只记录「谁对哪条做了什么」，不含凭证明文，便于追责又不扩大泄露面
type Audit struct {
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Label  string    `json:"label"`
	Action string    `json:"action"`
	Slot   string    `json:"slot"`
	Target string    `json:"target"`
}

// ---------------------------------------------------------------- 存储

type store struct {
	mu   sync.Mutex
	path string
	key  []byte // 非空则落盘为 AES-256-GCM 密文（state.bin）
	d    data
	subs map[string]map[chan struct{}]struct{}
}

// openStore：secret 非空时全量加密落盘（令牌、OAuth 回传数据都不再明文）。
// 若目录里还有旧的 state.json，会一次性迁移成 state.bin 并删除明文。
func openStore(dir, secret string) (*store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &store{subs: map[string]map[chan struct{}]struct{}{}}
	plain := filepath.Join(dir, "state.json")
	if secret != "" {
		sum := sha256.Sum256([]byte(secret))
		s.key = sum[:]
		s.path = filepath.Join(dir, "state.bin")
	} else {
		s.path = plain
	}

	raw, err := os.ReadFile(s.path)
	if err == nil {
		if s.key != nil {
			raw, err = decrypt(s.key, raw)
			if err != nil {
				return nil, fmt.Errorf("解密 %s 失败（secret 不匹配？）: %w", s.path, err)
			}
		}
		if err := json.Unmarshal(raw, &s.d); err != nil {
			return nil, fmt.Errorf("解析 %s: %w", s.path, err)
		}
		s.migrateUsernames()
		return s, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	// 迁移：有 secret 但只有旧的明文文件
	if s.key != nil {
		if old, err := os.ReadFile(plain); err == nil {
			if err := json.Unmarshal(old, &s.d); err != nil {
				return nil, fmt.Errorf("解析 %s: %w", plain, err)
			}
			s.migrateUsernames()
			s.mu.Lock()
			saveErr := s.saveLocked()
			s.mu.Unlock()
			if saveErr != nil {
				return nil, saveErr
			}
			_ = os.Remove(plain)
			return s, nil
		}
	}
	return s, nil
}

func (s *store) saveLocked() error {
	raw, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	if s.key != nil {
		if raw, err = encrypt(s.key, raw); err != nil {
			return err
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func encrypt(key, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, nil), nil
}

func decrypt(key, blob []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(blob) < gcm.NonceSize() {
		return nil, errors.New("密文长度不足")
	}
	nonce, body := blob[:gcm.NonceSize()], blob[gcm.NonceSize():]
	return gcm.Open(nil, nonce, body, nil)
}

func (s *store) notifyLocked(ch string) {
	for c := range s.subs[ch] {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

func (s *store) subscribe(ch string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := make(chan struct{}, 1)
	if s.subs[ch] == nil {
		s.subs[ch] = map[chan struct{}]struct{}{}
	}
	s.subs[ch][c] = struct{}{}
	return c
}

func (s *store) unsubscribe(ch string, c chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.subs[ch]; m != nil {
		delete(m, c)
		if len(m) == 0 {
			delete(s.subs, ch)
		}
	}
}

func newToken() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// normalizeUsername：小写、只留 a-z0-9._-，空格转 -
// normalizeUsername 保留 Unicode 字母与数字（因此中文用户名可用），
// 空格转 '-'，其它符号丢弃；长度按字符数限制在 32 以内。
func normalizeUsername(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	count := 0
	for _, r := range s {
		if count >= 32 {
			break
		}
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r), r == '.', r == '_', r == '-':
			b.WriteRune(r)
			count++
		case r == ' ' || r == '\t':
			b.WriteRune('-')
			count++
		}
	}
	return strings.Trim(b.String(), "-._")
}

// uniqueUsernameLocked 在锁内生成不冲突的用户名
func (s *store) uniqueUsernameLocked(base string) string {
	if base == "" {
		base = "user"
	}
	taken := func(name string) bool {
		for _, u := range s.d.Users {
			if strings.EqualFold(u.Username, name) {
				return true
			}
		}
		return false
	}
	if !taken(base) {
		return base
	}
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if !taken(cand) {
			return cand
		}
	}
	return base + "-" + newToken()[:4]
}

func (s *store) addUser(role, scope, label, username string) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := normalizeUsername(username)
	if want == "" {
		want = normalizeUsername(label)
	}
	if want == "" {
		want = "user-" + newToken()[:4] // 纯中文/符号标签：给一个可辨识的默认名
	}
	// 显式指定了用户名：先判断是否"根本没解析出可用字符"（纯符号/emoji），
	// 这与"已被占用"是两回事，必须分开报，否则中文/符号用户名总被说成占用。
	if username != "" {
		if normalizeUsername(username) == "" {
			return nil, errUsernameInvalid
		}
	}
	final := s.uniqueUsernameLocked(want)
	if username != "" && normalizeUsername(username) != final {
		return nil, errUsernameTaken
	}
	u := &User{
		ID: newToken()[:10], Username: final, Role: role, Scope: scope,
		Label: label, Token: newToken(), Created: time.Now().UTC(),
	}
	if role != "admin" {
		u.Scope = ""
	}
	s.d.Users = append(s.d.Users, u)
	if err := s.saveLocked(); err != nil {
		s.d.Users = s.d.Users[:len(s.d.Users)-1]
		return nil, err
	}
	cp := *u
	s.notifyLocked("admin")
	return &cp, nil
}

var (
	errUsernameTaken = errors.New("用户名已被占用")
	// 用户名规范化后为空（纯符号/emoji）与"已被占用"是两回事，必须分开报
	errUsernameInvalid = errors.New("用户名不合法")
)

// migrateUsernames：老数据没有 username，启动时补齐
func (s *store) migrateUsernames() {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, u := range s.d.Users {
		if u.Username != "" {
			continue
		}
		base := normalizeUsername(u.Label)
		if base == "" {
			base = normalizeUsername(u.Role)
		}
		if base == "" {
			base = "user"
		}
		u.Username = s.uniqueUsernameLocked(base)
		changed = true
	}
	if changed {
		_ = s.saveLocked()
	}
}

// userByUsername：按用户名解析（大小写不敏感）
func (s *store) userByUsername(name string) *User {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.d.Users {
		if strings.EqualFold(u.Username, name) || u.ID == name {
			cp := *u
			return &cp
		}
	}
	return nil
}

// rotateUserToken：换令牌 + 写审计在同一次落盘里完成。
// 任一步失败都整体回滚，绝不允许出现「旧链接已失效、审计没写、新链接也没交付」。
func (s *store) rotateUserToken(id string, actor *User) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.d.Users {
		if u.ID != id {
			continue
		}
		prev := *u
		prevAudit := len(s.d.Audit)
		u.Token = newToken()
		s.d.Audit = append(s.d.Audit, &Audit{
			At: time.Now().UTC(), Actor: actor.ID, Label: actor.Label,
			Action: "user.rotate", Target: u.ID + ":" + u.Label + ":" + u.Role,
		})
		if err := s.saveLocked(); err != nil {
			*u = prev
			s.d.Audit = s.d.Audit[:prevAudit]
			return nil, err
		}
		cp := *u
		s.notifyLocked("admin")
		s.notifyLocked("u:" + u.ID)
		return &cp, nil
	}
	return nil, os.ErrNotExist
}

func (s *store) updateUser(id string, fn func(*User) error) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.d.Users {
		if u.ID != id {
			continue
		}
		prev := *u
		if err := fn(u); err != nil {
			*u = prev
			return nil, err
		}
		if err := s.saveLocked(); err != nil {
			*u = prev
			return nil, err
		}
		cp := *u
		s.notifyLocked("admin")
		s.notifyLocked("u:" + u.ID)
		return &cp, nil
	}
	return nil, os.ErrNotExist
}

func (s *store) removeUser(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, u := range s.d.Users {
		if u.ID != id {
			continue
		}
		for _, sl := range s.d.Slots {
			if sl.Member == id || sl.Owner == id {
				return errors.New("该账号名下还有任务")
			}
		}
		s.d.Users = append(s.d.Users[:i], s.d.Users[i+1:]...)
		if err := s.saveLocked(); err != nil {
			return err
		}
		s.notifyLocked("admin")
		return nil
	}
	return os.ErrNotExist
}

// appendAudit 返回落盘错误：涉及敏感数据的操作必须据此 fail-closed
func (s *store) appendAudit(a *Audit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a.At = time.Now().UTC()
	s.d.Audit = append(s.d.Audit, a)
	if len(s.d.Audit) > 500 { // 只留最近 500 条
		s.d.Audit = s.d.Audit[len(s.d.Audit)-500:]
	}
	if err := s.saveLocked(); err != nil {
		// 落盘失败就回滚这条审计，避免内存与磁盘不一致
		s.d.Audit = s.d.Audit[:len(s.d.Audit)-1]
		return err
	}
	return nil
}

// auditBestEffort：失败不影响主流程（用于非敏感动作）
func (s *store) auditBestEffort(a *Audit) {
	_ = s.appendAudit(a)
}

func (s *store) audit(limit int) []Audit {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Audit, 0, limit)
	for i := len(s.d.Audit) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, *s.d.Audit[i])
	}
	return out
}

func (s *store) userByToken(tok string) *User {
	if tok == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.d.Users {
		if u.Token == tok {
			if u.Revoked { // 已吊销：一律拒绝，等同无效链接
				return nil
			}
			cp := *u
			return &cp
		}
	}
	return nil
}

func (s *store) userByID(id string) *User {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.d.Users {
		if u.ID == id {
			cp := *u
			return &cp
		}
	}
	return nil
}

func (s *store) users() []User {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]User, 0, len(s.d.Users))
	for _, u := range s.d.Users {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

func (s *store) createSlot(owner, member, label, link string) (*Slot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	sl := &Slot{
		ID: newToken()[:10], Owner: owner, Member: member, Label: label, Link: link,
		Created: now, Updated: now, Rev: 1, LinkGen: 1,
	}
	s.d.Slots = append(s.d.Slots, sl)
	if err := s.saveLocked(); err != nil {
		s.d.Slots = s.d.Slots[:len(s.d.Slots)-1]
		return nil, err
	}
	cp := *sl
	s.notifyLocked("admin")
	s.notifyLocked("u:" + member)
	return &cp, nil
}

func (s *store) slotsFor(memberID string) []Slot {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Slot
	for _, v := range s.d.Slots {
		if v.Member == memberID {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// allSlots：管理员看板默认不含已归档的
func (s *store) allSlots() []Slot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Slot, 0, len(s.d.Slots))
	for i := len(s.d.Slots) - 1; i >= 0; i-- {
		if s.d.Slots[i].Deleted {
			continue
		}
		out = append(out, *s.d.Slots[i])
	}
	return out
}

// exportSlots：导出用，含已归档的
func (s *store) exportSlots() []Slot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Slot, 0, len(s.d.Slots))
	for i := len(s.d.Slots) - 1; i >= 0; i-- {
		out = append(out, *s.d.Slots[i])
	}
	return out
}

// purgeArchived：清掉归档超过 N 天的任务（N=0 表示永不清理）
func (s *store) purgeArchived(olderThan time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.d.Slots[:0]
	removed := 0
	for _, v := range s.d.Slots {
		if v.Deleted && !v.DeletedAt.IsZero() && v.DeletedAt.Before(olderThan) {
			removed++
			continue
		}
		kept = append(kept, v)
	}
	if removed > 0 {
		s.d.Slots = kept
		_ = s.saveLocked()
		s.notifyLocked("admin")
	}
	return removed
}

func (s *store) slotByID(id string) *Slot {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.d.Slots {
		if v.ID == id {
			cp := *v
			return &cp
		}
	}
	return nil
}

func (s *store) updateSlot(id string, fn func(*Slot) error) (*Slot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.d.Slots {
		if v.ID != id {
			continue
		}
		prev := *v
		if err := fn(v); err != nil {
			*v = prev
			return nil, err
		}
		v.Updated = time.Now().UTC()
		v.Rev++
		if err := s.saveLocked(); err != nil {
			*v = prev
			return nil, err
		}
		cp := *v
		s.notifyLocked("admin")
		s.notifyLocked("u:" + v.Member)
		return &cp, nil
	}
	return nil, os.ErrNotExist
}

// archiveSlot：软删除——从 A 的看板消失，B 的历史里仍在
func (s *store) archiveSlot(id string) (*Slot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.d.Slots {
		if v.ID != id {
			continue
		}
		if v.Deleted {
			cp := *v
			return &cp, nil
		}
		v.Deleted = true
		v.DeletedAt = time.Now().UTC()
		v.Updated = v.DeletedAt
		if err := s.saveLocked(); err != nil {
			v.Deleted = false
			return nil, err
		}
		cp := *v
		s.notifyLocked("admin")
		s.notifyLocked("u:" + v.Member)
		return &cp, nil
	}
	return nil, os.ErrNotExist
}

// hardDeleteSlot：彻底删除（含 B 的历史）
func (s *store) hardDeleteSlot(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, v := range s.d.Slots {
		if v.ID != id {
			continue
		}
		m := v.Member
		s.d.Slots = append(s.d.Slots[:i], s.d.Slots[i+1:]...)
		if err := s.saveLocked(); err != nil {
			return err
		}
		s.notifyLocked("admin")
		s.notifyLocked("u:" + m)
		return nil
	}
	return os.ErrNotExist
}

func (s *store) removeSlot(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, v := range s.d.Slots {
		if v.ID != id {
			continue
		}
		m := v.Member
		s.d.Slots = append(s.d.Slots[:i], s.d.Slots[i+1:]...)
		if err := s.saveLocked(); err != nil {
			return err
		}
		s.notifyLocked("admin")
		s.notifyLocked("u:" + m)
		return nil
	}
	return os.ErrNotExist
}

// ---------------------------------------------------------------- 应用

type app struct {
	st      *store
	geo     *maxminddb.Reader
	trusted []netip.Prefix
	geoURL  string
	baseURL string
}

func main() {
	addr := flag.String("http", "127.0.0.1:8090", "监听地址")
	dir := flag.String("dir", "data", "数据目录")
	geoPath := flag.String("geo", "", "MMDB 国家库（可选）")
	trust := flag.String("trust-proxy", "", "可信反代 CIDR（可选）")
	geoURL := flag.String("geo-url", "", "客户端地域查询端点（可选，例如 https://ip.net.coffee/ ）")
	base := flag.String("base", "", "对外地址前缀，用于打印长期链接，例如 https://clip.example.com")
	secret := flag.String("secret", os.Getenv("AUTHCLIP_SECRET"), "静态加密口令；设置后 state 以 AES-256-GCM 落盘（建议用 AUTHCLIP_SECRET 环境变量）")
	retain := flag.Int("retain-days", 0, "已归档任务的保留天数；0=永久保留（B 端历史也随之清理）")
	flag.Parse()

	st, err := openStore(*dir, strings.TrimSpace(*secret))
	if err != nil {
		log.Fatal(err)
	}
	if strings.TrimSpace(*secret) == "" {
		log.Printf("警告：未设置 AUTHCLIP_SECRET，令牌与 OAuth 回传数据将以明文落盘（仅文件权限 0600 保护）")
	} else {
		log.Printf("静态加密已启用（AES-256-GCM）")
	}
	a := &app{st: st, geoURL: strings.TrimSpace(*geoURL), baseURL: strings.TrimRight(*base, "/")}
	for _, c := range strings.Split(*trust, ",") {
		if c = strings.TrimSpace(c); c != "" {
			p, err := netip.ParsePrefix(c)
			if err != nil {
				log.Fatalf("trust-proxy 非法 CIDR %q: %v", c, err)
			}
			a.trusted = append(a.trusted, p.Masked())
		}
	}
	if *geoPath != "" {
		r, err := maxminddb.Open(*geoPath)
		if err != nil {
			log.Fatalf("打开 MMDB 失败: %v", err)
		}
		a.geo = r
		defer func() { _ = r.Close() }()
	}

	// 归档任务的保留期（0=永久）。到期会连 B 端历史一起清掉。
	if *retain > 0 {
		purge := func() {
			if n := st.purgeArchived(time.Now().UTC().AddDate(0, 0, -*retain)); n > 0 {
				log.Printf("已清理 %d 条归档任务（保留 %d 天）", n, *retain)
			}
		}
		purge()
		go func() {
			for range time.Tick(6 * time.Hour) {
				purge()
			}
		}()
		log.Printf("归档保留期：%d 天", *retain)
	} else {
		log.Printf("归档保留期：永久（可用 -retain-days 设定）")
	}

	// 子命令：users / add
	args := flag.Args()
	if len(args) > 0 {
		a.cli(args)
		return
	}

	handler, err := a.routes()
	if err != nil {
		log.Fatal(err)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0), // 不写客户端地址
	}
	log.Printf("AuthClip %s 监听 http://%s", appVersion, *addr)
	log.Fatal(srv.ListenAndServe())
}

// routes 单独抽出来，便于测试用 httptest 直接挂上去
func (a *app) routes() (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/config", a.config)
	mux.HandleFunc("GET /api/me", a.wrap(a.me))
	mux.HandleFunc("GET /api/board", a.wrap(a.board))
	mux.HandleFunc("POST /api/members", a.wrap(a.addMember))
	mux.HandleFunc("POST /api/slots", a.wrap(a.createSlot))
	mux.HandleFunc("PATCH /api/slots/{id}", a.wrap(a.patchSlot))
	mux.HandleFunc("POST /api/slots/{id}/clear", a.wrap(a.clearSlot))
	mux.HandleFunc("DELETE /api/slots/{id}", a.wrap(a.deleteSlot))
	mux.HandleFunc("GET /api/mine", a.wrap(a.mine))
	mux.HandleFunc("GET /api/ipcheck", a.wrap(a.ipCheck))
	mux.HandleFunc("GET /api/slots/{id}/card.svg", a.wrap(a.slotCard))
	mux.HandleFunc("POST /api/slots/{id}/report", a.wrap(a.report))
	mux.HandleFunc("GET /api/stream", a.wrap(a.stream))
	// 账号与权限（需要 all 范围）
	mux.HandleFunc("GET /api/users", a.wrap(a.listUsers))
	mux.HandleFunc("POST /api/users", a.wrap(a.createUser))
	mux.HandleFunc("PATCH /api/users/{id}", a.wrap(a.patchUser))
	mux.HandleFunc("GET /api/users/{id}/link", a.wrap(a.userLink))
	mux.HandleFunc("POST /api/users/{id}/rotate", a.wrap(a.rotateUser))
	mux.HandleFunc("DELETE /api/users/{id}", a.wrap(a.deleteUser))
	mux.HandleFunc("GET /api/audit", a.wrap(a.auditLog))
	mux.HandleFunc("GET /api/export", a.wrap(a.exportSlots))

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		return nil, err
	}
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.Handle("/", a.static(static))
	return a.headers(mux), nil
}

// ---------------------------------------------------------------- CLI

func (a *app) cli(args []string) {
	switch args[0] {
	case "users":
		for _, u := range a.st.users() {
			state := ""
			if u.Revoked {
				state = "  [已吊销]"
			}
			fmt.Printf("%-6s %-4s %-12s %-16s %s%s\n", u.Role, u.scopeOf(), u.Username, u.ID, u.Label, state)
		}
	case "add":
		if len(args) < 3 || (args[1] != "admin" && args[1] != "member") {
			fmt.Println("用法: authclip add admin|member 标签 [all|own]")
			os.Exit(2)
		}
		scope := ""
		labelParts := args[2:]
		if args[1] == "admin" && len(labelParts) > 1 {
			last := labelParts[len(labelParts)-1]
			if last == "all" || last == "own" {
				scope = last
				labelParts = labelParts[:len(labelParts)-1]
			}
		}
		if args[1] == "admin" && scope == "" {
			scope = "all"
		}
		uname := ""
		if args[1] == "member" && len(labelParts) > 1 {
			uname = labelParts[len(labelParts)-1]
			labelParts = labelParts[:len(labelParts)-1]
		}
		u, err := a.st.addUser(args[1], scope, strings.Join(labelParts, " "), uname)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("已创建 %s「%s」范围=%s\n长期链接: %s\n", u.Role, u.Label, u.scopeOf(), a.linkFor(u))
	case "link":
		if len(args) < 3 {
			fmt.Println("用法: authclip link admin|member 标签")
			os.Exit(2)
		}
		for _, u := range a.st.users() {
			if u.Role == args[1] && u.Label == strings.Join(args[2:], " ") {
				fmt.Println(a.linkFor(&u))
				return
			}
		}
		fmt.Println("没找到该用户")
		os.Exit(1)
	default:
		fmt.Println("子命令: users | add | link")
	}
}

func (a *app) linkFor(u *User) string {
	prefix := a.baseURL
	if prefix == "" {
		prefix = "http://127.0.0.1:8090"
	}
	if u.Role == "admin" {
		return prefix + "/#a=" + u.Token
	}
	return prefix + "/#b=" + u.Token
}

// ---------------------------------------------------------------- 中间件

func (a *app) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (a *app) static(root fs.FS) http.Handler {
	files := http.FileServer(http.FS(root))
	// 资源版本号：内容一变 URL 就变，浏览器/中间层不可能再复用旧的或坏掉的缓存
	ver := assetVersion(root)
	index := renderIndex(root, ver)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			conn := "connect-src 'self'"
			if origin := originOf(a.geoURL); origin != "" {
				conn += " " + origin
			}
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; "+
					conn+"; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		}
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(index)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// assetVersion 以样式与脚本的内容哈希作为版本号
func assetVersion(root fs.FS) string {
	h := sha256.New()
	for _, name := range []string{"style.css", "app.js"} {
		if b, err := fs.ReadFile(root, name); err == nil {
			_, _ = h.Write(b)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}

// renderIndex 给 index.html 里的资源引用补上 ?v=哈希
func renderIndex(root fs.FS, ver string) []byte {
	b, err := fs.ReadFile(root, "index.html")
	if err != nil {
		return []byte("<!doctype html><title>AuthClip</title>")
	}
	out := strings.ReplaceAll(string(b), `href="style.css"`, `href="style.css?v=`+ver+`"`)
	out = strings.ReplaceAll(out, `src="app.js"`, `src="app.js?v=`+ver+`"`)
	return []byte(out)
}

func originOf(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

type handlerFunc func(http.ResponseWriter, *http.Request, *User)

func (a *app) wrap(h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 只认请求头。历史上曾支持 ?t= 兜底（给 EventSource 用），
		// 那会让长期令牌落进访问日志/浏览器历史，已移除。
		u := a.st.userByToken(r.Header.Get("X-Token"))
		if u == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "链接无效"})
			return
		}
		h(w, r, u)
	}
}

// ---------------------------------------------------------------- 接口

func (a *app) config(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"geo_url": a.geoURL})
}

func (a *app) me(w http.ResponseWriter, r *http.Request, u *User) {
	writeJSON(w, http.StatusOK, map[string]string{
		"id": u.ID, "username": u.Username, "role": u.Role, "label": u.Label, "scope": u.scopeOf(),
	})
}

func (a *app) board(w http.ResponseWriter, r *http.Request, u *User) {
	if u.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "仅管理员"})
		return
	}
	all := u.canSeeAll()
	members := []map[string]any{}
	for _, m := range a.st.users() {
		if m.Role != "member" {
			continue
		}
		// 长期链接不再随看板下发：需要时用 rotate 重新签发（一次性显示），
		// 避免任何列表接口成为令牌批量出口。
		members = append(members, map[string]any{
			"id": m.ID, "username": m.Username, "label": m.Label,
			"revoked": m.Revoked, "created": m.Created,
		})
	}

	slots := a.st.allSlots()
	if !all {
		own := make([]Slot, 0, len(slots))
		for _, s := range slots {
			if s.Owner == u.ID {
				own = append(own, s)
			}
		}
		slots = own
	}

	// 归档（A 端删除的任务）：单独给一份，前端折叠展示；接收人侧已经不再下发
	archived := make([]Slot, 0)
	for _, s := range a.st.exportSlots() {
		if !s.Deleted {
			continue
		}
		if !all && s.Owner != u.ID {
			continue
		}
		archived = append(archived, s)
	}

	resp := map[string]any{"members": members, "slots": slots, "archived": archived, "scope": u.scopeOf()}
	if all {
		users := []map[string]any{}
		for _, x := range a.st.users() {
			users = append(users, map[string]any{
				"id": x.ID, "username": x.Username, "role": x.Role, "scope": x.scopeOf(),
				"label": x.Label, "revoked": x.Revoked, "created": x.Created, "self": x.ID == u.ID,
			})
		}
		resp["users"] = users
		resp["audit"] = a.st.audit(50)
	}
	writeJSON(w, http.StatusOK, resp)
}

// guardSlot：own 范围的管理员只能动自己创建的任务
func (a *app) guardSlot(w http.ResponseWriter, u *User, slotID string) *Slot {
	sl := a.st.slotByID(slotID)
	if sl == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "任务不存在"})
		return nil
	}
	if !u.canSeeAll() && sl.Owner != u.ID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "任务不存在"})
		return nil
	}
	return sl
}

func (a *app) addMember(w http.ResponseWriter, r *http.Request, u *User) {
	if u.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "仅管理员"})
		return
	}
	// 建人 = 分发一条拥有该接收人全部权限的长期链接。
	// 只有全量管理员能建，避免受限管理员自造身份。
	if !u.canSeeAll() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "创建接收人需要全量权限，请联系全量管理员"})
		return
	}
	var body struct {
		Label    string `json:"label"`
		Username string `json:"username"`
	}
	if err := decode(w, r, &body); err != nil {
		return
	}
	label := strings.TrimSpace(body.Label)
	if label == "" {
		label = "未命名接收人"
	}
	if len(label) > 40 {
		label = label[:40]
	}
	m, err := a.st.addUser("member", "", label, body.Username)
	if err != nil {
		if errors.Is(err, errUsernameInvalid) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "用户名不合法：至少要有 1 个字母/数字/汉字"})
		} else if errors.Is(err, errUsernameTaken) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "用户名已被占用"})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "写入失败"})
		}
		return
	}
	a.st.appendAudit(&Audit{Actor: u.ID, Label: u.Label, Action: "user.create", Target: m.ID + ":" + m.Label})
	// 链接只在创建这一次完整返回；own 范围管理员后续无法再取到
	writeJSON(w, http.StatusOK, map[string]any{
		"id": m.ID, "username": m.Username, "label": m.Label, "token": m.Token, "link": a.linkFor(m),
	})
}

// ---------------------------------------------------------------- 账号与权限

func (a *app) listUsers(w http.ResponseWriter, r *http.Request, u *User) {
	if !u.canSeeAll() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "需要全量权限的管理员"})
		return
	}
	// 绝不回传 token：列表只给身份与状态，需要链接就 rotate 重新签发
	out := make([]map[string]any, 0)
	for _, x := range a.st.users() {
		out = append(out, map[string]any{
			"id": x.ID, "username": x.Username, "role": x.Role, "scope": x.scopeOf(),
			"label": x.Label, "revoked": x.Revoked, "created": x.Created,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// userLink：按单条取一次长期链接（全量管理员）。
// 列表接口永远不带令牌，避免"一次请求拉走全部身份"；
// 这里按需取单条，并且每次都留审计。
func (a *app) userLink(w http.ResponseWriter, r *http.Request, u *User) {
	if !u.canSeeAll() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "需要全量权限的管理员"})
		return
	}
	target := a.st.userByID(r.PathValue("id"))
	if target == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "账号不存在"})
		return
	}
	if target.Revoked {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "该账号已吊销，恢复后再取链接"})
		return
	}
	// 只允许读「接收人」的链接：接收人链接只代表该接收人自己的任务。
	// 管理员链接代表身份本身——读出来就能在该账号名下操作，
	// 审计会记到被冒用者头上，事后无法分辨。管理员需要恢复访问时走 rotate
	// （旧链接立即失效、动作记在操作者名下），由操作者私下把新链接交给本人。
	if target.Role != "member" {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "管理员链接不可读取；如需恢复某管理员的访问，请用「重置链接」（旧链接立即失效，操作会记在你名下）",
		})
		return
	}
	// 底线：已吊销不给（上面已判）、每次取都落审计、审计失败就不给。
	targetDesc := target.ID + ":" + target.Label + ":" + target.Role
	if target.Role == "admin" {
		targetDesc += ":" + target.scopeOf()
	}
	if err := a.st.appendAudit(&Audit{
		Actor: u.ID, Label: u.Label, Action: "user.link", Target: targetDesc,
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "审计写入失败，已拒绝返回链接",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"id": target.ID, "username": target.Username, "label": target.Label, "role": target.Role,
		"link": a.linkFor(target),
	})
}

func (a *app) createUser(w http.ResponseWriter, r *http.Request, u *User) {
	if !u.canSeeAll() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "需要全量权限的管理员"})
		return
	}
	var body struct {
		Role     string `json:"role"`
		Scope    string `json:"scope"`
		Label    string `json:"label"`
		Username string `json:"username"`
	}
	if err := decode(w, r, &body); err != nil {
		return
	}
	role := strings.TrimSpace(body.Role)
	if role != "admin" && role != "member" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "role 只能是 admin 或 member"})
		return
	}
	scope := strings.TrimSpace(body.Scope)
	if role == "admin" && scope != "own" {
		scope = "all" // 默认给全量；要收敛必须显式传 own
	}
	label := strings.TrimSpace(body.Label)
	if label == "" {
		label = "未命名"
	}
	if len(label) > 40 {
		label = label[:40]
	}
	nu, err := a.st.addUser(role, scope, label, body.Username)
	if err != nil {
		if errors.Is(err, errUsernameInvalid) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "用户名不合法：至少要有 1 个字母/数字/汉字"})
		} else if errors.Is(err, errUsernameTaken) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "用户名已被占用"})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "写入失败"})
		}
		return
	}
	a.st.appendAudit(&Audit{Actor: u.ID, Label: u.Label, Action: "user.create", Target: nu.ID + ":" + nu.Label})
	writeJSON(w, http.StatusOK, map[string]any{
		"id": nu.ID, "username": nu.Username, "role": nu.Role, "scope": nu.scopeOf(),
		"label": nu.Label, "link": a.linkFor(nu), "token": nu.Token,
	})
}

func (a *app) patchUser(w http.ResponseWriter, r *http.Request, u *User) {
	if !u.canSeeAll() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "需要全量权限的管理员"})
		return
	}
	var body struct {
		Label    *string `json:"label"`
		Username *string `json:"username"`
		Scope    *string `json:"scope"`
		Revoke   *bool   `json:"revoke"`
	}
	if err := decode(w, r, &body); err != nil {
		return
	}
	id := r.PathValue("id")
	target := a.st.userByID(id)
	if target == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "账号不存在"})
		return
	}
	if body.Scope != nil && target.Role != "admin" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "接收人没有 scope"})
		return
	}
	if body.Revoke != nil && *body.Revoke && target.ID == u.ID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不能吊销自己"})
		return
	}
	upd, err := a.st.updateUser(id, func(x *User) error {
		if body.Label != nil {
			v := strings.TrimSpace(*body.Label)
			if len(v) > 40 {
				v = v[:40]
			}
			x.Label = v
		}
		if body.Username != nil {
			want := normalizeUsername(*body.Username)
			if len([]rune(want)) < 1 {
				return errUsernameInvalid
			}
			for _, other := range a.st.d.Users {
				if other.ID != x.ID && strings.EqualFold(other.Username, want) {
					return errUsernameTaken
				}
			}
			x.Username = want
		}
		if body.Scope != nil {
			if strings.TrimSpace(*body.Scope) == "own" {
				x.Scope = "own"
			} else {
				x.Scope = "all"
			}
		}
		if body.Revoke != nil {
			x.Revoked = *body.Revoke
		}
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "账号不存在"})
		return
	}
	action := "user.update"
	if body.Revoke != nil {
		action = "user.revoke"
		if !*body.Revoke {
			action = "user.restore"
		}
	}
	a.st.appendAudit(&Audit{Actor: u.ID, Label: u.Label, Action: action, Target: id + ":" + upd.Label})
	writeJSON(w, http.StatusOK, map[string]any{
		"id": upd.ID, "role": upd.Role, "scope": upd.scopeOf(), "label": upd.Label, "revoked": upd.Revoked,
	})
}

// rotateUser：换一条长期链接，旧链接立即失效
func (a *app) rotateUser(w http.ResponseWriter, r *http.Request, u *User) {
	if !u.canSeeAll() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "需要全量权限的管理员"})
		return
	}
	id := r.PathValue("id")
	if id == u.ID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不能轮换自己"})
		return
	}
	// 全局管理员可重置任何账号的链接（含其他管理员）；
	// 唯一例外是不能重置自己——那只会让自己当前链接失效，属于误操作。
	if target := a.st.userByID(id); target == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "账号不存在"})
		return
	}
	var fresh *User
	fresh, err := a.st.rotateUserToken(id, u)
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "账号不存在"})
		return
	}
	if err != nil {
		// 原子写入失败 = 什么都没改：旧链接仍然有效，可以安全重试
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "轮换未生效（写入失败），旧链接保持不变，可稍后重试",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": fresh.ID, "username": fresh.Username, "label": fresh.Label,
		"link": a.linkFor(fresh), "token": fresh.Token,
		"warning": "旧链接已立即失效；请私下把新链接交给本人，此操作已记在你名下",
	})
}

func (a *app) deleteUser(w http.ResponseWriter, r *http.Request, u *User) {
	if !u.canSeeAll() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "需要全量权限的管理员"})
		return
	}
	id := r.PathValue("id")
	if id == u.ID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不能删除自己"})
		return
	}
	if err := a.st.removeUser(id); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	a.st.appendAudit(&Audit{Actor: u.ID, Label: u.Label, Action: "user.delete", Target: id})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *app) auditLog(w http.ResponseWriter, r *http.Request, u *User) {
	if !u.canSeeAll() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "需要全量权限的管理员"})
		return
	}
	writeJSON(w, http.StatusOK, a.st.audit(100))
}

// exportSlots：A 端批量导出（CSV / JSON），含已归档任务
//
// 默认脱敏：凭证/邮箱/IP 只给形状，不给原值。要完整凭据必须显式
// ?secrets=1&confirm=EXPORT，且两种模式都会在写出前落审计。
func (a *app) exportSlots(w http.ResponseWriter, r *http.Request, u *User) {
	if !u.canSeeAll() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "需要全量权限的管理员"})
		return
	}
	// 管理员要看账号密码：默认给明文；需要打码版加 ?masked=1（两种都留审计）
	full := r.URL.Query().Get("masked") != "1"
	if r.URL.Query().Get("secrets") == "1" && r.URL.Query().Get("confirm") != "EXPORT" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "导出完整凭据需要 ?secrets=1&confirm=EXPORT 二次确认",
		})
		return
	}

	label := map[string]string{}
	for _, x := range a.st.users() {
		label[x.ID] = x.Label
	}
	slots := a.st.exportSlots()
	stamp := time.Now().Format("20060102-150405")
	mode := "masked"
	if full {
		mode = "full"
	}

	// 审计先写，再吐数据：写不进去就拒绝导出（fail-closed）
	if err := a.st.appendAudit(&Audit{
		Actor: u.ID, Label: u.Label, Action: "data.export",
		Target: fmt.Sprintf("%d rows mode=%s", len(slots), mode),
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "审计写入失败，已拒绝导出（避免无记录的敏感数据外流）",
		})
		return
	}

	pick := func(v string, mask func(string) string) string {
		if full {
			return v
		}
		return mask(v)
	}

	if r.URL.Query().Get("format") == "json" {
		if !full {
			red := make([]map[string]any, 0, len(slots))
			for _, s := range slots {
				red = append(red, map[string]any{
					"id": s.ID, "label": s.Label, "member": label[s.Member], "owner": label[s.Owner],
					"email": maskEmail(s.Email), "code": maskCode(s.Code),
					"password":   maskSecret(s.Password),
					"reg_region": s.RegRegion, "reg_ip": maskIP(s.RegIP),
					"seen_ip": maskIP(s.SeenIP), "node_ip": maskNode(s.NodeIP),
					"tz": s.TZ, "lang": s.Lang,
					"created": s.Created, "submitted": s.Submitted, "deleted": s.Deleted,
					"redacted": true,
				})
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="authclip-%s.json"`, stamp))
			_ = json.NewEncoder(w).Encode(red)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="authclip-%s-full.json"`, stamp))
		_ = json.NewEncoder(w).Encode(slots)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="authclip-%s-%s.csv"`, stamp, mode))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{
		"id", "label", "状态", "接收人", "下发人", "邮箱", "密码", "凭证", "注册地区",
		"注册IP", "观测IP", "节点IP", "信任分", "IP类型", "风控标记", "ASN组织", "城市", "rDNS", "时区", "语言", "来源校验",
		"创建时间", "回传时间", "已归档", "导出模式",
	})
	for _, s := range slots {
		state := "待回传"
		if s.Code != "" || s.Email != "" {
			state = "已回传"
		}
		if s.Deleted {
			state = "已归档·" + state
		}
		row := []string{
			s.ID, s.Label, state, label[s.Member], label[s.Owner],
			pick(s.Email, maskEmail), pick(s.Password, maskSecret), pick(s.Code, maskCode),
			s.RegRegion, pick(s.RegIP, maskIP), pick(s.SeenIP, maskIP), pick(s.NodeIP, maskNode),
			riskScore(s.Risk), riskKind(s.Risk), riskFlags(s.Risk), riskOrg(s.Risk), riskCity(s.Risk), riskRDNS(s.Risk),
			s.TZ, s.Lang, verifiedText(s.Unverified),
			s.Created.Format("2006-01-02 15:04"), fmtTime(s.Submitted),
			boolText(s.Deleted), mode,
		}
		for i, cell := range row {
			row[i] = spreadsheetText(cell)
		}
		_ = cw.Write(row)
	}
	cw.Flush()
}

// spreadsheetText keeps formula-like values as text when opening the CSV in a spreadsheet.
// ---- IP 画像的文本化（导出与前端共用同一套判定语义）----

func verifiedText(unverified bool) string {
	if unverified {
		return "未校验"
	}
	return "已校验"
}

func riskScore(r *IPRisk) string {
	if r == nil {
		return ""
	}
	return strconv.Itoa(r.TrustScore)
}

func riskKind(r *IPRisk) string {
	if r == nil {
		return ""
	}
	switch {
	case r.IsResidential:
		return "住宅"
	case r.IsDatacenter:
		return "机房"
	case r.Kind == "mobile":
		return "移动"
	case r.Kind != "":
		return r.Kind
	}
	return "未知"
}

func riskFlags(r *IPRisk) string {
	if r == nil {
		return ""
	}
	var f []string
	if r.IsVPN {
		f = append(f, "VPN")
	}
	if r.IsProxy {
		f = append(f, "代理")
	}
	if r.IsTor {
		f = append(f, "Tor")
	}
	if r.IsCrawler {
		f = append(f, "爬虫")
	}
	if r.IsAbuser {
		f = append(f, "滥用")
	}
	if r.IsDatacenter {
		f = append(f, "数据中心")
	}
	if len(f) == 0 {
		return "无"
	}
	return strings.Join(f, "/")
}

func riskOrg(r *IPRisk) string {
	if r == nil {
		return ""
	}
	if r.ASN > 0 {
		return fmt.Sprintf("%s (AS%d)", r.Org, r.ASN)
	}
	return r.Org
}

func riskCity(r *IPRisk) string {
	if r == nil {
		return ""
	}
	if r.City == "" {
		return r.Country
	}
	return r.Country + " / " + r.City
}

func riskRDNS(r *IPRisk) string {
	if r == nil {
		return ""
	}
	return r.RDNS
}

// clip 截断到 n 个字节以内（按 rune 边界，避免切坏多字节字符）
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	for len(string(r)) > n && len(r) > 0 {
		r = r[:len(r)-1]
	}
	return string(r)
}

func spreadsheetText(value string) string {
	first := strings.TrimLeftFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || r == '\ufeff'
	})
	if strings.HasPrefix(value, "\t") || strings.HasPrefix(value, "\r") ||
		strings.HasPrefix(value, "\n") ||
		(first != "" && strings.ContainsRune("=+-@＝＋－＠", []rune(first)[0])) {
		return "'" + value
	}
	return value
}

// 脱敏：保留可辨识的形状，去掉可用性
// maskSecret 密码类字段：默认导出只留首字符
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= 2 {
		return "••"
	}
	return string(r[0]) + "••••"
}

func maskCode(s string) string {
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= 6 {
		return "***"
	}
	return string(runes[:4]) + "……" + string(runes[len(runes)-2:]) + fmt.Sprintf("（%d 字符）", len(runes))
}

func maskEmail(s string) string {
	if s == "" {
		return ""
	}
	at := strings.LastIndex(s, "@")
	if at <= 0 {
		return "***"
	}
	local, domain := s[:at], s[at:]
	head := local
	if len([]rune(head)) > 2 {
		head = string([]rune(head)[:2])
	}
	return head + "***" + domain
}

// maskNode 节点/代理串可能不是纯 IP（ip:port:user:pass、socks5://…），按字符串打码
func maskNode(v string) string {
	if v == "" {
		return ""
	}
	if ip, err := netip.ParseAddr(v); err == nil {
		return maskIP(ip.Unmap().String())
	}
	r := []rune(v)
	if len(r) <= 8 {
		return "***"
	}
	return string(r[:8]) + "***"
}

func maskIP(s string) string {
	if s == "" {
		return ""
	}
	if ip, err := netip.ParseAddr(s); err == nil {
		if ip.Is4() {
			b := ip.As4()
			return fmt.Sprintf("%d.%d.x.x", b[0], b[1])
		}
		return "IPv6"
	}
	return "***"
}

func fmtTime(t time.Time) string {
	if t.IsZero() || t.Year() < 2000 {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}

func boolText(b bool) string {
	if b {
		return "是"
	}
	return "否"
}

func (a *app) createSlot(w http.ResponseWriter, r *http.Request, u *User) {
	if u.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "仅管理员"})
		return
	}
	var body struct {
		Member string `json:"member"`
		Link   string `json:"link"`
		Label  string `json:"label"`
	}
	if err := decode(w, r, &body); err != nil {
		return
	}
	m := a.st.userByUsername(strings.TrimSpace(body.Member))
	if m == nil || m.Role != "member" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "接收人不存在（请检查用户名）"})
		return
	}
	link := strings.TrimSpace(body.Link)
	if link == "" || len(link) > maxLink || !plausibleURL(link) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "授权链接格式不正确"})
		return
	}
	// 没有 state 就无法把回传绑定到这一次授权，直接拒收
	if linkStateOf(link) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "授权链接缺少 state 参数，无法校验回调归属，请使用带 state 的授权链接",
		})
		return
	}
	label := strings.TrimSpace(body.Label)
	if len(label) > 60 {
		label = label[:60]
	}
	sl, err := a.st.createSlot(u.ID, m.ID, label, link)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "写入失败"})
		return
	}
	a.st.appendAudit(&Audit{Actor: u.ID, Label: u.Label, Action: "slot.create", Slot: sl.ID, Target: m.Label})
	writeJSON(w, http.StatusOK, sl)
}

func (a *app) patchSlot(w http.ResponseWriter, r *http.Request, u *User) {
	if u.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "仅管理员"})
		return
	}
	if a.guardSlot(w, u, r.PathValue("id")) == nil {
		return
	}
	var body struct {
		Link       *string `json:"link"`
		Label      *string `json:"label"`
		Member     *string `json:"member"`
		NodeIP     *string `json:"node_ip"`
		Region     *string `json:"region"`
		Email      *string `json:"email"`
		KeepResult *bool   `json:"keep_result"` // 仅当链接写错但结果仍有效时显式保留
	}
	if err := decode(w, r, &body); err != nil {
		return
	}
	if body.Link != nil {
		v := strings.TrimSpace(*body.Link)
		if v == "" || len(v) > maxLink || !plausibleURL(v) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "授权链接格式不正确"})
			return
		}
		if linkStateOf(v) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "授权链接缺少 state 参数，无法校验回调归属",
			})
			return
		}
	}
	if body.Member != nil {
		m := a.st.userByUsername(strings.TrimSpace(*body.Member))
		if m == nil || m.Role != "member" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "接收人不存在（请检查用户名）"})
			return
		}
	}
	if body.NodeIP != nil {
		if _, ok := normalizeNode(*body.NodeIP); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "节点/代理内容过长或含非法字符"})
			return
		}
	}
	if body.Region != nil {
		v := strings.ToUpper(strings.TrimSpace(*body.Region))
		if v != "" && !validRegion(v) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "地区请填两位国家代码"})
			return
		}
	}
	if body.Email != nil {
		v := strings.TrimSpace(*body.Email)
		if v != "" && !validEmail(v) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "邮箱格式不正确"})
			return
		}
	}
	relinkKept := false
	sl, err := a.st.updateSlot(r.PathValue("id"), func(s *Slot) error {
		if body.Link != nil {
			next := strings.TrimSpace(*body.Link)
			// 换链接 = 新一轮授权。判定用「链接是否变化」而不是「state 是否变化」：
			// 新链接沿用同一个 state 时，旧回传同样失效，必须清掉。
			if next != s.Link {
				s.LinkGen++ // 代次递增：清空与否都要递增，旧请求据此失效
				if body.KeepResult != nil && *body.KeepResult {
					// 显式保留（管理员认旧结果仍属于本轮），会单独留痕
					relinkKept = true
				} else {
					clearReturned(s)
				}
			}
			s.Link = next
		}
		if body.Label != nil {
			v := strings.TrimSpace(*body.Label)
			if len(v) > 60 {
				v = v[:60]
			}
			s.Label = v
		}
		if body.Member != nil {
			next := strings.TrimSpace(*body.Member)
			// 改派 = 换人：旧接收人的凭证/邮箱/IP 一律抹掉，
			// 否则新接收人一打开自己的长期链接就能读到上一位的数据。
			if next != s.Member {
				s.Member = next
				clearReturned(s)
			}
		}
		if body.NodeIP != nil {
			if v, ok := normalizeNode(*body.NodeIP); ok {
				s.NodeIP = v
			}
		}
		if body.Region != nil {
			s.RegRegion = strings.ToUpper(strings.TrimSpace(*body.Region))
		}
		if body.Email != nil {
			s.Email = strings.TrimSpace(*body.Email)
		}
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "槽位不存在"})
		return
	}
	action := "slot.update"
	switch {
	case body.Member != nil:
		action = "slot.reassign"
	case body.Link != nil && relinkKept:
		action = "slot.relink.keep" // 换了链接但显式保留了旧回传
	case body.Link != nil:
		action = "slot.relink"
	}
	a.st.appendAudit(&Audit{Actor: u.ID, Label: u.Label, Action: action, Slot: sl.ID})
	writeJSON(w, http.StatusOK, sl)
}

func (a *app) clearSlot(w http.ResponseWriter, r *http.Request, u *User) {
	if u.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "仅管理员"})
		return
	}
	if a.guardSlot(w, u, r.PathValue("id")) == nil {
		return
	}
	sl, err := a.st.updateSlot(r.PathValue("id"), func(s *Slot) error {
		clearReturned(s)
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "槽位不存在"})
		return
	}
	a.st.appendAudit(&Audit{Actor: u.ID, Label: u.Label, Action: "slot.clear", Slot: sl.ID})
	writeJSON(w, http.StatusOK, sl)
}

// clearReturned 清空一条任务里由接收人回传/由服务端观测到的全部数据
func clearReturned(s *Slot) {
	s.Email, s.Code, s.Callback, s.StateParam = "", "", "", ""
	s.Password = ""
	s.Risk = nil
	s.TZ, s.Lang = "", ""
	s.Unverified = false
	s.RegIP, s.RegRegion, s.NodeIP = "", "", ""
	s.SeenIP, s.SeenRegion = "", ""
	s.Submitted = time.Time{}
}

func (a *app) deleteSlot(w http.ResponseWriter, r *http.Request, u *User) {
	if u.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "仅管理员"})
		return
	}
	if a.guardSlot(w, u, r.PathValue("id")) == nil {
		return
	}
	id := r.PathValue("id")

	// 默认归档：B 仍能在历史里看到；?purge=1 才彻底删除（需全量权限）
	if r.URL.Query().Get("purge") == "1" {
		if !u.canSeeAll() {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "彻底删除需要全量权限"})
			return
		}
		if err := a.st.hardDeleteSlot(id); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "槽位不存在"})
			return
		}
		a.st.appendAudit(&Audit{Actor: u.ID, Label: u.Label, Action: "slot.purge", Slot: id})
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "purged": true})
		return
	}

	if _, err := a.st.archiveSlot(id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "槽位不存在"})
		return
	}
	a.st.appendAudit(&Audit{Actor: u.ID, Label: u.Label, Action: "slot.archive", Slot: id})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "archived": true})
}

func (a *app) mine(w http.ResponseWriter, r *http.Request, u *User) {
	if u.Role != "member" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "请用接收人链接打开"})
		return
	}
	ip := a.observed(r) // 仅 member 路径
	// B 端只拿「够用」的信息：提交成功后凭证类字段一律不下发，
	// 这样即使抓包/换设备，接收人也拿不到自己交上去的 code 与密码。
	slots := a.st.slotsFor(u.ID)
	redacted := make([]map[string]any, 0, len(slots))
	for _, s := range slots {
		if s.Deleted {
			continue // 归档后不再下发给接收人：历史只在 A 端保留
		}
		redacted = append(redacted, map[string]any{
			"id": s.ID, "label": s.Label, "link": s.Link, "link_gen": s.LinkGen,
			"submitted": s.Submitted, "deleted": s.Deleted,
			"email":      maskEmail(s.Email), // 只留打码，便于确认交给过谁
			"reg_region": s.RegRegion, "seen_region": s.SeenRegion,
			"created": s.Created, "updated": s.Updated,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"role": "member", "label": u.Label, "username": u.Username,
		"slots": redacted,
		"seen":  map[string]string{"ip": ipText(ip), "region": a.country(ip)},
	})
}

// 出口 IP 画像：按 IP 向 ip.net.coffee 的公开接口查询（geoip + iprisk），
// 结果存进任务里，供 A 端判断账号注册环境的风控特征。
// 这些接口带 Access-Control-Allow-Origin: *，但我们统一由服务端调用：
// 浏览器侧会被广告拦截器/隐私插件影响，且无法跨域读 SVG 卡片。

type IPRisk struct {
	IP            string    `json:"ip"`
	CIDR          string    `json:"cidr"`
	Kind          string    `json:"kind"` // residential / hosting / mobile …
	TrustScore    int       `json:"trust_score"`
	IsVPN         bool      `json:"is_vpn"`
	IsProxy       bool      `json:"is_proxy"`
	IsTor         bool      `json:"is_tor"`
	IsCrawler     bool      `json:"is_crawler"`
	IsAbuser      bool      `json:"is_abuser"`
	IsDatacenter  bool      `json:"is_datacenter"`
	IsResidential bool      `json:"is_residential"`
	ASN           int       `json:"asn"`
	Org           string    `json:"org"`
	Country       string    `json:"country"`
	City          string    `json:"city"`
	RDNS          string    `json:"rdns"`
	AbuserScore   string    `json:"abuser_score"`
	Source        string    `json:"source"`
	CheckedAt     time.Time `json:"checked_at"`
	Err           string    `json:"err,omitempty"`
}

// ipriskJSON 对应上游返回的字段（只取需要的）
type ipriskJSON struct {
	IP            string `json:"ip"`
	CIDR          string `json:"cidr"`
	IsDatacenter  bool   `json:"is_datacenter"`
	IsResidential bool   `json:"isResidential"`
	IsVPN         bool   `json:"is_vpn"`
	IsProxy       bool   `json:"is_proxy"`
	IsTor         bool   `json:"is_tor"`
	IsCrawler     bool   `json:"is_crawler"`
	IsAbuser      bool   `json:"is_abuser"`
	ASN           int    `json:"asn"`
	AsOrg         string `json:"asOrganization"`
	Country       string `json:"country"`
	City          string `json:"city"`
	RDNS          string `json:"rdns"`
	AbuserScore   string `json:"abuser_score"`
	TrustScore    int    `json:"trust_score"`
	ASNKind       string `json:"asn_kind"`
	CompanyType   string `json:"company_type"`
}

var ipRiskClient = &http.Client{Timeout: 6 * time.Second}

// riskCache 按 IP 缓存 10 分钟：B 端页面轮询 / 多次提交都不会反复打上游
var (
	riskMu    sync.Mutex
	riskCache = map[string]riskEntry{}
	riskSem   = make(chan struct{}, 4) // 上游并发上限
	riskGate  sync.Map                 // B 端 /api/ipcheck 的每用户节流
)

type riskEntry struct {
	risk *IPRisk
	at   time.Time
}

func (a *app) lookupRisk(ip string) *IPRisk {
	if os.Getenv("AUTHCLIP_IPRISK") == "0" {
		return nil // 用 AUTHCLIP_IPRISK=0 可整体关闭上游查询
	}
	ip = strings.TrimSpace(ip)
	if net.ParseIP(ip) == nil {
		return nil
	}
	riskMu.Lock()
	if e, ok := riskCache[ip]; ok {
		ttl := 30 * time.Minute
		if e.risk != nil && e.risk.Err != "" {
			ttl = 2 * time.Minute // 上游出错时短缓存，避免雪崩式重试
		}
		if time.Since(e.at) < ttl {
			riskMu.Unlock()
			return e.risk
		}
	}
	riskMu.Unlock()

	// 并发闸门：同一时刻最多 4 个上游请求，防止突发流量把我们和上游一起拖垮
	select {
	case riskSem <- struct{}{}:
		defer func() { <-riskSem }()
	case <-time.After(3 * time.Second):
		return &IPRisk{IP: ip, Source: "ip.net.coffee", CheckedAt: time.Now().UTC(), Err: "排队超时"}
	}

	out := &IPRisk{IP: ip, Source: "ip.net.coffee", CheckedAt: time.Now().UTC()}
	var raw ipriskJSON
	if err := httpGetJSON(ipRiskClient, "https://ip.net.coffee/api/iprisk/"+url.PathEscape(ip), &raw); err == nil {
		out.CIDR = raw.CIDR
		out.TrustScore = raw.TrustScore
		out.IsVPN, out.IsProxy, out.IsTor = raw.IsVPN, raw.IsProxy, raw.IsTor
		out.IsCrawler, out.IsAbuser = raw.IsCrawler, raw.IsAbuser
		out.IsDatacenter, out.IsResidential = raw.IsDatacenter, raw.IsResidential
		out.ASN, out.Org = raw.ASN, raw.AsOrg
		out.Country, out.City = raw.Country, raw.City
		out.RDNS, out.AbuserScore = raw.RDNS, raw.AbuserScore
		out.Kind = raw.ASNKind
		switch {
		case out.Kind == "" && raw.CompanyType != "":
			out.Kind = raw.CompanyType
		case out.Kind == "" && raw.IsDatacenter:
			out.Kind = "hosting"
		case out.Kind == "" && raw.IsResidential:
			out.Kind = "residential"
		}
	} else {
		out.Err = "上游查询失败"
	}

	// 地理信息缺失时用 geoip 端点补
	if out.Country == "" || out.City == "" {
		var geo struct {
			Country     string `json:"country"`
			CountryCode string `json:"country_code"`
			City        string `json:"city"`
			ISP         string `json:"isp"`
		}
		if err := httpGetJSON(ipRiskClient, "https://ip.net.coffee/api/geoip/"+url.PathEscape(ip), &geo); err == nil {
			if out.Country == "" {
				out.Country = geo.Country
			}
			if out.City == "" {
				out.City = geo.City
			}
			if out.Org == "" {
				out.Org = geo.ISP
			}
		}
	}

	riskMu.Lock()
	riskCache[ip] = riskEntry{risk: out, at: time.Now()}
	riskMu.Unlock()
	return out
}

func httpGetJSON(c *http.Client, u string, dst any) error {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "AuthClip/"+appVersion)
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(dst)
}

// enrichRisk 提交成功后异步补画像：不阻塞 B 的提交，也不影响主流程
func (a *app) enrichRisk(slotID, ip string) {
	risk := a.lookupRisk(ip)
	if risk == nil {
		return
	}
	_, _ = a.st.updateSlot(slotID, func(s *Slot) error {
		if s.Deleted {
			return errors.New("archived")
		}
		s.Risk = risk
		return nil
	})
}

// ipCheck 给 B 端看自己的出口 IP 画像（提交前预览，提交时会再查一次并落库）
// 自建 IP 卡片（SVG）：数据来自我们已存的 Risk，不依赖第三方渲染，
// 也不会出现"显示的是查看者自己 IP"的问题（card.net.coffee 的卡片就是那样）。

func (a *app) slotCard(w http.ResponseWriter, r *http.Request, u *User) {
	sl := a.guardSlot(w, u, r.PathValue("id"))
	if sl == nil {
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="authclip-`+sl.ID+`.svg"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(renderCardSVG(sl)))
}

func esc(s string) string {
	return html.EscapeString(s)
}

func renderCardSVG(s *Slot) string {
	r := s.Risk
	ip, trust, kind, flags, org, place := "—", "—", "—", "—", "—", "—"
	tone := "#8b988f"
	if r != nil {
		ip = r.IP
		trust = strconv.Itoa(r.TrustScore)
		kind = riskKind(r)
		flags = riskFlags(r)
		org = riskOrg(r)
		place = riskCity(r)
		switch {
		case r.TrustScore >= 80:
			tone = "#146b4a" // 干净
		case r.TrustScore >= 50:
			tone = "#8a6d1f" // 一般
		default:
			tone = "#a3342c" // 风险高
		}
	}
	label := s.Label
	if label == "" {
		label = "授权任务"
	}
	when := ""
	if !s.Submitted.IsZero() {
		when = s.Submitted.Local().Format("2006-01-02 15:04")
	}

	rows := [][2]string{
		{"IP", ip},
		{"信任分", trust},
		{"IP 类型", kind},
		{"风控标记", flags},
		{"ASN / 组织", org},
		{"城市", place},
		{"回传时间", when},
	}

	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="560" height="300" viewBox="0 0 560 300" font-family="-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif">`)
	b.WriteString(`<rect width="560" height="300" rx="16" fill="#f6f8f5"/>`)
	b.WriteString(`<rect x="1" y="1" width="558" height="298" rx="15" fill="none" stroke="#dfe6dc"/>`)
	// 标题
	b.WriteString(`<circle cx="34" cy="36" r="9" fill="#146b4a"/>`)
	b.WriteString(`<text x="52" y="41" font-size="15" font-weight="600" fill="#1c2a22">AuthClip</text>`)
	b.WriteString(`<text x="118" y="41" font-size="13" fill="#8b988f">出口 IP 画像</text>`)
	b.WriteString(`<text x="528" y="41" font-size="12" fill="#8b988f" text-anchor="end">` + esc(label) + `</text>`)
	// 信任分大数字
	b.WriteString(`<text x="34" y="104" font-size="13" fill="#8b988f">信任分</text>`)
	b.WriteString(`<text x="34" y="150" font-size="42" font-weight="700" fill="` + tone + `">` + esc(trust) + `</text>`)
	b.WriteString(`<text x="34" y="176" font-size="13" fill="` + tone + `">` + esc(kind) + `</text>`)
	// 右侧明细
	y := 76
	for _, kv := range rows {
		if kv[0] == "信任分" || kv[0] == "IP 类型" {
			continue
		}
		b.WriteString(`<text x="230" y="` + strconv.Itoa(y) + `" font-size="11.5" fill="#8b988f">` + esc(kv[0]) + `</text>`)
		b.WriteString(`<text x="330" y="` + strconv.Itoa(y) + `" font-size="12" fill="#1c2a22">` + esc(kv[1]) + `</text>`)
		y += 26
	}
	b.WriteString(`<text x="34" y="278" font-size="10.5" fill="#a8b3ac">数据来源 ip.net.coffee · 采集于 ` + esc(time.Now().Local().Format("2006-01-02 15:04")) + `</text>`)
	b.WriteString(`</svg>`)
	return b.String()
}

func (a *app) ipCheck(w http.ResponseWriter, r *http.Request, u *User) {
	if u.Role != "member" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "仅接收人可用"})
		return
	}
	// 每用户 5 秒最多一次：页面反复刷新不会把上游打成筛子（缓存之外的第二道闸）
	if last, ok := riskGate.Load(u.ID); ok {
		if t, _ := last.(time.Time); time.Since(t) < 5*time.Second {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "查询过于频繁，请稍后重试"})
			return
		}
	}
	riskGate.Store(u.ID, time.Now())
	ip := a.observed(r)
	risk := a.lookupRisk(ipText(ip))
	if risk == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ip": ipText(ip), "region": a.country(ip)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ip": risk.IP, "region": a.country(ip), "risk": risk})
}

// looksLikeAuthURL 判断用户是不是把「授权链接本身」当回传内容交上来了
// normalizeNode 处理「静态/动态家宽节点」的各种写法：
//
//	154.196.210.126 / 154.196.210.126:7778 / 154.196.210.126:7778:user:pass
//	socks5://user:pass@host:1080 / host.example.com:8080
//
// 能解析成纯 IP 就规范化；否则原样保留（限长、禁控制字符）。
func normalizeNode(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", true
	}
	if len(v) > 200 || strings.ContainsAny(v, "\r\n\t") {
		return "", false
	}
	if ip, err := netip.ParseAddr(v); err == nil {
		return ip.Unmap().String(), true
	}
	return v, true
}

func looksLikeAuthURL(s string) bool {
	return strings.Contains(s, "claude.ai/oauth") || strings.Contains(s, "code_challenge") ||
		strings.Contains(s, "response_type=") || strings.Contains(s, "redirect_uri=")
}

func (a *app) report(w http.ResponseWriter, r *http.Request, u *User) {
	if u.Role != "member" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "仅接收人可回传"})
		return
	}
	sl := a.st.slotByID(r.PathValue("id"))
	if sl == nil || sl.Member != u.ID || sl.Deleted {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "任务不存在"})
		return
	}
	var body struct {
		Email     string `json:"email"`
		Password  string `json:"password"` // 有些账号要密码登录，选填
		Code      string `json:"code"`
		Callback  string `json:"callback"`
		RegIP     string `json:"reg_ip"`
		RegRegion string `json:"reg_region"`
		NodeIP    string `json:"node_ip"`
		TZ        string `json:"tz"`
		Lang      string `json:"lang"`
		LinkGen   *int   `json:"link_gen"` // 必须回传页面看到的代次
	}
	if err := decode(w, r, &body); err != nil {
		return
	}
	email := strings.TrimSpace(body.Email)
	password := strings.TrimSpace(body.Password)
	tz := clip(strings.TrimSpace(body.TZ), 64)
	lang := clip(strings.TrimSpace(body.Lang), 32)
	if len(password) > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "密码过长"})
		return
	}
	callback := strings.TrimSpace(body.Callback)
	code := strings.TrimSpace(body.Code)
	stateParam := ""

	// 两种回传都接受：
	//   1) 整条回调链接（含 code 与 state）—— 可校验归属
	//   2) 只有授权码 —— 放行，但落库时标记「来源未校验」
	// 把内容塞进 code 框（有人会粘错位置）按同样规则处理。
	if callback == "" {
		callback = code
		code = ""
	}
	if strings.Contains(callback, "code=") {
		c, st, ok := parseCallback(callback)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "无法从这条链接里取出 code，请确认粘的是回调链接",
			})
			return
		}
		code, stateParam = c, st
	} else {
		if looksLikeAuthURL(callback) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "这是授权链接本身，不是回传内容；请先完成授权再回传",
			})
			return
		}
		if len(callback) < 16 || strings.ContainsAny(callback, " \t\r\n") {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "内容无法识别：请粘贴整条回调链接，或单独粘贴授权码（无空格）",
			})
			return
		}
		code, callback, stateParam = callback, "", ""
	}

	regIP := strings.TrimSpace(body.RegIP)
	regRegion := strings.ToUpper(strings.TrimSpace(body.RegRegion))
	node := strings.TrimSpace(body.NodeIP)

	if email == "" || !validEmail(email) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "注册邮箱不正确"})
		return
	}
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "没有识别到凭证：请粘贴整条回调链接"})
		return
	}
	if len(code) > 4096 || len(callback) > maxLink {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "内容过长"})
		return
	}
	if regIP != "" {
		ip, err := netip.ParseAddr(regIP)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "注册 IP 格式不正确"})
			return
		}
		regIP = ip.Unmap().String()
	}
	if regRegion != "" && !validRegion(regRegion) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "注册地区请填两位国家代码"})
		return
	}
	if v, ok := normalizeNode(node); ok {
		node = v
	} else {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "节点/代理内容过长或含非法字符"})
		return
	}

	// 代次校验：换过链接就必须重新取页面，否则旧请求会把上一轮结果写回来
	if body.LinkGen == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "缺少 link_gen：请刷新页面后重新提交",
		})
		return
	}
	if *body.LinkGen != sl.LinkGen {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "授权链接已更换，请刷新页面查看新链接后重试",
		})
		return
	}

	// 服务端核对 OAuth 归属：回调里的 state 必须与当前授权链接里的 state 一致。
	// 链接本身没有 state 时不再「跳过校验」，而是直接拒绝（无 state 即无法绑定）。
	// 只交 code 的情况（stateParam == ""）跳过归属校验，落库时标记「未校验」
	want := linkStateOf(sl.Link)
	if stateParam != "" && want == "" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "该任务的授权链接缺少 state，无法校验回调归属，请管理员更新链接",
		})
		return
	}
	if stateParam != "" && stateParam != want {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "回调的 state 与当前授权链接不一致，可能拿错了链接（或该任务已改派/换过链接）",
		})
		return
	}

	observed := a.observed(r)
	seenRegion := a.country(observed)

	var denied bool
	updated, err := a.st.updateSlot(sl.ID, func(s *Slot) error {
		// 事务内再校验一次接收人：读与写之间可能刚被改派或归档
		if s.Member != u.ID || s.Deleted {
			denied = true
			return errors.New("reassigned or archived")
		}
		// 提交期间可能刚被换链接：代次变了就直接拒（同 state 也能拦住）
		if s.LinkGen != *body.LinkGen {
			denied = true
			return errors.New("link generation changed")
		}
		// 链接在提交期间被换掉：按新链接重新比一次
		if stateParam != "" {
			if cur := linkStateOf(s.Link); cur == "" || cur != stateParam {
				denied = true
				return errors.New("link changed")
			}
		}
		s.Email, s.Code, s.Callback, s.StateParam = email, code, callback, stateParam
		s.Password = password
		s.TZ, s.Lang = tz, lang
		s.Unverified = stateParam == ""
		s.NodeIP = node
		s.RegIP = regIP
		if s.RegIP == "" {
			s.RegIP = ipText(observed)
		}
		s.RegRegion = regRegion
		if s.RegRegion == "" {
			s.RegRegion = seenRegion
		}
		s.SeenIP, s.SeenRegion = ipText(observed), seenRegion
		s.Submitted = time.Now().UTC()
		return nil
	})
	if denied {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "任务在提交期间发生变化（已改派或已换链接），请刷新后重试",
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存失败"})
		return
	}
	a.st.appendAudit(&Audit{Actor: u.ID, Label: u.Label, Action: "slot.report", Slot: updated.ID})
	if s := updated; s != nil && s.RegIP != "" {
		go a.enrichRisk(s.ID, s.RegIP) // 异步补 IP 画像，不拖慢提交
	}
	writeJSON(w, http.StatusOK, updated)
}

// ---------------------------------------------------------------- 流

func (a *app) stream(w http.ResponseWriter, r *http.Request, u *User) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "不支持流"})
		return
	}
	ch := "admin"
	if u.Role == "member" {
		ch = "u:" + u.ID
	}
	sub := a.st.subscribe(ch)
	defer a.st.unsubscribe(ch, sub)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 2000\n\n")
	flusher.Flush()

	tick := time.NewTicker(sessionEC)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub:
			fmt.Fprint(w, "event: change\ndata: {}\n\n")
			flusher.Flush()
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// ---------------------------------------------------------------- 网络与地域

func (a *app) observed(r *http.Request) netip.Addr {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	ip := peer.Addr().Unmap()
	for _, p := range a.trusted {
		if p.Contains(ip) {
			if fwd, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
				ip = fwd.Unmap()
			}
			break
		}
	}
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || !ip.IsGlobalUnicast() {
		return netip.Addr{}
	}
	return ip
}

func ipText(ip netip.Addr) string {
	if !ip.IsValid() {
		return ""
	}
	return ip.String()
}

func (a *app) country(ip netip.Addr) string {
	if a.geo == nil || !ip.IsValid() {
		return ""
	}
	var code string
	if err := a.geo.Lookup(ip).DecodePath(&code, "country", "iso_code"); err != nil {
		return ""
	}
	code = strings.ToUpper(code)
	if len(code) != 2 {
		return ""
	}
	return code
}

// ---------------------------------------------------------------- 解析与校验

// linkStateOf 取授权链接里的 state（OAuth 绑定的一次性随机串），只做字符串解析
func linkStateOf(link string) string {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(u.Query().Get("state"))
}

// parseCallback 从「整条回调链接」或「code=…&state=…」里切出 code 与 state。
// 只做字符串解析，不发起任何请求。
func parseCallback(raw string) (code, state string, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", false
	}
	if len(s) > maxLink {
		s = s[:maxLink]
	}
	if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.Host != "" {
		q := u.Query()
		// 授权链接本身也带 code=true / code_challenge / response_type，
		// 必须先排除，否则会把 code=true 当成凭证。
		if q.Get("code_challenge") != "" || q.Get("response_type") != "" || q.Get("redirect_uri") != "" || q.Get("scope") != "" {
			return "", "", false
		}
		if c := q.Get("code"); plausibleCode(c) {
			return c, q.Get("state"), true
		}
		return "", "", false
	}
	if strings.Contains(s, "code=") {
		if v, err := url.ParseQuery(s); err == nil {
			if c := v.Get("code"); plausibleCode(c) {
				return c, v.Get("state"), true
			}
		}
	}
	return "", "", false
}

// plausibleCode 排除 OAuth 授权链接里的布尔标记（code=true 之类）
func plausibleCode(c string) bool {
	c = strings.TrimSpace(c)
	switch strings.ToLower(c) {
	case "", "true", "false", "1", "0":
		return false
	}
	return true
}

func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	body := http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不正确"})
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func validEmail(s string) bool {
	if len(s) > 254 {
		return false
	}
	p, err := mail.ParseAddress(s)
	return err == nil && p.Address == s
}

func validRegion(v string) bool {
	if len(v) != 2 {
		return false
	}
	for i := 0; i < 2; i++ {
		if v[i] < 'A' || v[i] > 'Z' {
			return false
		}
	}
	return true
}

// plausibleURL 只做字面校验，绝不请求
func plausibleURL(s string) bool {
	if !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "http://") {
		return false
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	host := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		host = rest[:i]
	}
	return host != "" && !strings.ContainsAny(host, " \t\r\n")
}

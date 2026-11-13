// Package auth 实现轻量的个人/家庭访问保护：设备登录优先，接不上就用 DKStore 自有密码（实施文档 §10.1 / §10.1.1）。
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kukushouhou/DKStore/internal/hostauth"
	"github.com/kukushouhou/DKStore/internal/store"
)

const (
	cookieName       = "dkstore_session"
	deviceCookieName = "dkstore_device_session"
	passwordSetKey   = "admin_password_hash"

	// MinPasswordLen 是管理员密码的最小长度（字符数）。
	MinPasswordLen = 6

	deviceTicketTTL  = 30 * time.Second
	deviceSessionTTL = 12 * time.Hour
)

// 访问模式（GET /auth/status 的 mode 字段）。
const (
	ModeDevice            = "device"
	ModePassword          = "password"
	ModeSetupRequired     = "setup_required"
	ModeHostLoginRequired = "host_login_required"
)

// 错误（其 Error() 即 API 错误码）。
var (
	ErrInvalidCredentials = errors.New("invalid_credentials")
	ErrPasswordSet        = errors.New("password_already_set")
	ErrSetupRequired      = errors.New("setup_required")
	ErrPasswordTooShort   = errors.New("password_too_short")
)

// HostVerifier 核验宿主设备（群晖 DSM / OpenWrt LuCI）的管理员登录态。
type HostVerifier interface {
	InstallMode() string
	Supported() bool
	Verify(ctx context.Context, r *http.Request) hostauth.Result
}

// Service 管理管理员密码、登录会话与宿主登录态复用。
//
// 判定规则：
//   - 已设置 DKStore 密码 → 统一走密码会话；
//   - 未设置密码且为群晖/OpenWrt 套件安装、能核验到宿主管理员登录态 → 设备登录直接放行；
//   - 其他情况 → 必须先设置密码（setup_required），所有业务接口拒绝访问。
type Service struct {
	st             *store.Store
	host           HostVerifier
	mu             sync.RWMutex
	sessions       map[string]time.Time
	deviceTickets  map[string]time.Time
	deviceSessions map[string]time.Time

	// getHash/setHash 可在测试中替换；默认读写 setting 表。
	getHash func(ctx context.Context) string
	setHash func(ctx context.Context, hash string, onlyIfAbsent bool) (bool, error)
}

// StatusInfo 是 GET /auth/status 的结果。
type StatusInfo struct {
	Mode          string
	Authenticated bool
	InstallMode   string
	HostUser      string
	// HintCode 为宿主登录态核验失败的原因码（仅群晖/OpenWrt 套件安装且 setup_required 时给出）。
	HintCode string
}

// New 创建认证服务。host 为 nil 时按运行环境自动构建宿主核验器。
func New(st *store.Store, host HostVerifier) *Service {
	if host == nil {
		host = hostauth.New("", nil, nil)
	}
	s := &Service{
		st:             st,
		host:           host,
		sessions:       make(map[string]time.Time),
		deviceTickets:  make(map[string]time.Time),
		deviceSessions: make(map[string]time.Time),
	}
	s.getHash = s.dbGetHash
	s.setHash = s.dbSetHash
	return s
}

// PasswordSet 返回是否已设置 DKStore 自有密码。
func (s *Service) PasswordSet(ctx context.Context) bool {
	return s.getHash(ctx) != ""
}

// Status 计算当前请求的访问模式与认证状态。
//
// 规则：
//  1. 未设置管理员密码（未完成首次初始化）：
//     - 若为宿主套件安装且宿主核验器可用，必须先通过宿主登录态核验才允许进入 ModeSetupRequired；
//       未登录宿主后台的其他浏览器返回 ModeHostLoginRequired（防止未登录访客抢先初始化）。
//     - 其他环境（Docker / 独立二进制）直接返回 ModeSetupRequired。
//  2. 已设置管理员密码（已完成初始化）：
//     - 若持有有效密码会话 Cookie → ModePassword, Authenticated=true；
//     - 若来自可信宿主后台（host.Supported && Verify.OK）→ ModeDevice, Authenticated=true（免二次登录）；
//     - 其他浏览器 / 外部链接访问 → ModePassword, Authenticated=false（展示密码登录页）。
func isZeroSetupDeviceMode(mode string) bool {
	return mode == hostauth.ModeFnOS || mode == hostauth.ModeOpenWrt
}

// IssueDeviceTicket 签发一次性短效设备登录交接票据（仅允许受信任本机网关 Socket 调用，例如 OpenWrt LuCI rpcd）。
func (s *Service) IssueDeviceTicket() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	now := time.Now()
	s.mu.Lock()
	for k, exp := range s.deviceTickets {
		if now.After(exp) {
			delete(s.deviceTickets, k)
		}
	}
	s.deviceTickets[tok] = now.Add(deviceTicketTTL)
	s.mu.Unlock()
	return tok
}

// ConsumeDeviceTicket 核销一次性设备交接票据并下发宿主设备会话 Cookie。
func (s *Service) ConsumeDeviceTicket(w http.ResponseWriter, ticket string) bool {
	if len(ticket) < 16 || len(ticket) > 128 {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	exp, ok := s.deviceTickets[ticket]
	if ok {
		delete(s.deviceTickets, ticket)
	}
	for k, e := range s.deviceTickets {
		if now.After(e) {
			delete(s.deviceTickets, k)
		}
	}
	if !ok || now.After(exp) {
		s.mu.Unlock()
		return false
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	sessTok := hex.EncodeToString(b)
	sessExp := now.Add(deviceSessionTTL)
	for k, e := range s.deviceSessions {
		if now.After(e) {
			delete(s.deviceSessions, k)
		}
	}
	s.deviceSessions[sessTok] = sessExp
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     deviceCookieName,
		Value:    sessTok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  sessExp,
	})
	return true
}

func (s *Service) hasDeviceSession(r *http.Request) bool {
	if r == nil {
		return false
	}
	c, err := r.Cookie(deviceCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	s.mu.RLock()
	exp, ok := s.deviceSessions[c.Value]
	s.mu.RUnlock()
	return ok && time.Now().Before(exp)
}

func (s *Service) Status(ctx context.Context, r *http.Request) StatusInfo {
	info := StatusInfo{InstallMode: s.host.InstallMode()}
	if !s.PasswordSet(ctx) {
		if s.host.Supported() {
			if s.hasDeviceSession(r) {
				if isZeroSetupDeviceMode(s.host.InstallMode()) {
					info.Mode = ModeDevice
					info.Authenticated = true
					info.HostUser = "root"
					return info
				}
				info.Mode = ModeSetupRequired
				info.HostUser = "admin"
				return info
			}
			res := s.host.Verify(ctx, r)
			if res.OK {
				if isZeroSetupDeviceMode(s.host.InstallMode()) {
					info.Mode = ModeDevice
					info.Authenticated = true
					info.HostUser = res.User
					return info
				}
				info.Mode = ModeSetupRequired
				info.HostUser = res.User
				return info
			}
			if res.Reason != hostauth.ReasonUnavailable && res.Reason != hostauth.ReasonUnsupportedMode {
				info.Mode = ModeHostLoginRequired
				info.HintCode = res.Reason
				return info
			}
		}
		info.Mode = ModeSetupRequired
		return info
	}

	if s.hasSession(r) {
		info.Mode = ModePassword
		info.Authenticated = true
		return info
	}
	if s.host.Supported() {
		if s.hasDeviceSession(r) {
			info.Mode = ModeDevice
			info.Authenticated = true
			info.HostUser = "root"
			return info
		}
		if res := s.host.Verify(ctx, r); res.OK {
			info.Mode = ModeDevice
			info.Authenticated = true
			info.HostUser = res.User
			return info
		}
	}
	info.Mode = ModePassword
	info.Authenticated = false
	return info
}

// Authorize 判断请求能否访问业务接口；不通过时返回 API 错误码（unauthorized / setup_required）。
func (s *Service) Authorize(ctx context.Context, r *http.Request) (ok bool, code string) {
	if !s.PasswordSet(ctx) {
		if isZeroSetupDeviceMode(s.host.InstallMode()) && s.host.Supported() {
			if s.hasDeviceSession(r) || s.host.Verify(ctx, r).OK {
				return true, ""
			}
		}
		return false, ErrSetupRequired.Error()
	}
	if s.hasSession(r) {
		return true, ""
	}
	if s.host.Supported() && (s.hasDeviceSession(r) || s.host.Verify(ctx, r).OK) {
		return true, ""
	}
	return false, "unauthorized"
}

// IsAuthenticated 判断请求是否通过认证（等价于 Authorize 的 ok）。
func (s *Service) IsAuthenticated(ctx context.Context, r *http.Request) bool {
	ok, _ := s.Authorize(ctx, r)
	return ok
}

func (s *Service) hasSession(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return false
	}
	s.mu.RLock()
	exp, ok := s.sessions[c.Value]
	s.mu.RUnlock()
	return ok && time.Now().Before(exp)
}

// Login 验证密码并下发 HttpOnly Cookie。未设置密码时返回 ErrSetupRequired。
func (s *Service) Login(ctx context.Context, w http.ResponseWriter, password string) error {
	hash := s.getHash(ctx)
	if hash == "" {
		return ErrSetupRequired
	}
	if !verifyPassword(password, hash) {
		return ErrInvalidCredentials
	}
	s.issueCookie(w)
	return nil
}

// Setup 首次设置管理员密码并直接登录；已设置密码时返回 ErrPasswordSet。
func (s *Service) Setup(ctx context.Context, w http.ResponseWriter, password string) error {
	if err := checkPassword(password); err != nil {
		return err
	}
	if s.PasswordSet(ctx) {
		return ErrPasswordSet
	}
	ok, err := s.setHash(ctx, hashPassword(password), true)
	if err != nil {
		return err
	}
	if !ok {
		return ErrPasswordSet
	}
	s.issueCookie(w)
	return nil
}

// SetPassword 修改管理员密码：必须已设置密码并验证旧密码。
// 修改成功后清除所有已有会话并为当前请求重新签发会话。
// 未设置密码时返回 ErrSetupRequired（首次设置统一走 Setup）。
func (s *Service) SetPassword(ctx context.Context, w http.ResponseWriter, oldPassword, newPassword string) error {
	curHash := s.getHash(ctx)
	if curHash == "" {
		return ErrSetupRequired
	}
	if !verifyPassword(oldPassword, curHash) {
		return ErrInvalidCredentials
	}
	if err := checkPassword(newPassword); err != nil {
		return err
	}
	if _, err := s.setHash(ctx, hashPassword(newPassword), false); err != nil {
		return err
	}
	s.mu.Lock()
	s.sessions = make(map[string]time.Time)
	s.mu.Unlock()
	s.issueCookie(w)
	return nil
}

func checkPassword(pw string) error {
	if utf8.RuneCountInString(pw) < MinPasswordLen {
		return ErrPasswordTooShort
	}
	return nil
}

// Logout 清除当前会话。
func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	if dc, err := r.Cookie(deviceCookieName); err == nil && dc.Value != "" {
		s.mu.Lock()
		delete(s.deviceSessions, dc.Value)
		s.mu.Unlock()
	}
	for _, name := range []string{cookieName, deviceCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
		})
	}
}

func (s *Service) issueCookie(w http.ResponseWriter) {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	exp := time.Now().Add(30 * 24 * time.Hour)
	s.mu.Lock()
	now := time.Now()
	for k, e := range s.sessions {
		if now.After(e) {
			delete(s.sessions, k)
		}
	}
	s.sessions[tok] = exp
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
	})
}

func (s *Service) dbGetHash(ctx context.Context) string {
	var h string
	_ = s.st.DB.QueryRowContext(ctx, `SELECT v FROM setting WHERE k=?`, passwordSetKey).Scan(&h)
	return h
}

func (s *Service) dbSetHash(ctx context.Context, hash string, onlyIfAbsent bool) (bool, error) {
	q := `INSERT INTO setting(k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`
	if onlyIfAbsent {
		// 并发首次设置时只有一个请求能成功写入
		q = `INSERT INTO setting(k, v) VALUES (?, ?) ON CONFLICT(k) DO NOTHING`
	}
	res, err := s.st.DB.ExecContext(ctx, q, passwordSetKey, hash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func hashPassword(pw string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	mac := hmac.New(sha256.New, salt)
	mac.Write([]byte(pw))
	return hex.EncodeToString(salt) + ":" + hex.EncodeToString(mac.Sum(nil))
}

func verifyPassword(pw, stored string) bool {
	if len(stored) < 33 || stored[32] != ':' {
		return false
	}
	salt, err := hex.DecodeString(stored[:32])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, salt)
	mac.Write([]byte(pw))
	want := hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(want), []byte(stored[33:])) == 1
}

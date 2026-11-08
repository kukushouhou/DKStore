// Package auth 实现轻量的个人/家庭管理员密码保护与 Cookie 会话（实施文档 §10.1 / §15）。
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

	"github.com/kukushouhou/DKStore/internal/store"
)

const (
	cookieName     = "dkstore_session"
	passwordSetKey = "admin_password_hash"
)

// Service 管理管理员密码与登录会话。
// 若用户尚未设置管理员密码（passwordEnabled=false），默认允许本机/局域网直接访问并在界面提示可设置密码；
// 一旦设置了密码，则所有 API 请求必须携带有效会话 Cookie。
type Service struct {
	st       *store.Store
	mu       sync.RWMutex
	sessions map[string]time.Time
}

// New 创建认证服务。
func New(st *store.Store) *Service {
	return &Service{
		st:       st,
		sessions: make(map[string]time.Time),
	}
}

// Status 返回当前是否已启用密码保护，以及当前请求是否已认证通过。
func (s *Service) Status(ctx context.Context, r *http.Request) (passwordEnabled, authenticated bool) {
	hash := s.getPasswordHash(ctx)
	if hash == "" {
		return false, true
	}
	return true, s.IsAuthenticated(ctx, r)
}

// IsAuthenticated 判断请求是否通过认证。
func (s *Service) IsAuthenticated(ctx context.Context, r *http.Request) bool {
	hash := s.getPasswordHash(ctx)
	if hash == "" {
		return true
	}
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return false
	}
	s.mu.RLock()
	exp, ok := s.sessions[c.Value]
	s.mu.RUnlock()
	return ok && time.Now().Before(exp)
}

// Login 验证密码并下发 HttpOnly Cookie。
func (s *Service) Login(ctx context.Context, w http.ResponseWriter, password string) error {
	hash := s.getPasswordHash(ctx)
	if hash == "" {
		return nil
	}
	if !verifyPassword(password, hash) {
		return errors.New("invalid_credentials")
	}
	s.issueCookie(w)
	return nil
}

// SetPassword 设置或修改管理员密码（若已有密码需验证 oldPassword；newPassword 为空表示关闭密码保护）。
func (s *Service) SetPassword(ctx context.Context, w http.ResponseWriter, oldPassword, newPassword string) error {
	curHash := s.getPasswordHash(ctx)
	if curHash != "" && !verifyPassword(oldPassword, curHash) {
		return errors.New("invalid_credentials")
	}
	if newPassword == "" {
		_, err := s.st.DB.ExecContext(ctx, `DELETE FROM setting WHERE k=?`, passwordSetKey)
		return err
	}
	newHash := hashPassword(newPassword)
	_, err := s.st.DB.ExecContext(ctx, `
		INSERT INTO setting(k, v) VALUES (?, ?)
		ON CONFLICT(k) DO UPDATE SET v=excluded.v`,
		passwordSetKey, newHash)
	if err != nil {
		return err
	}
	s.issueCookie(w)
	return nil
}

// Logout 清除当前会话。
func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (s *Service) issueCookie(w http.ResponseWriter) {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	exp := time.Now().Add(30 * 24 * time.Hour)
	s.mu.Lock()
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

func (s *Service) getPasswordHash(ctx context.Context) string {
	var h string
	_ = s.st.DB.QueryRowContext(ctx, `SELECT v FROM setting WHERE k=?`, passwordSetKey).Scan(&h)
	return h
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

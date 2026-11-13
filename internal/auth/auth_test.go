package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kukushouhou/DKStore/internal/hostauth"
)

type fakeHost struct {
	mode string
	res  hostauth.Result
}

func (f fakeHost) InstallMode() string { return f.mode }
func (f fakeHost) Supported() bool {
	return f.mode == hostauth.ModeSynology || f.mode == hostauth.ModeFnOS || f.mode == hostauth.ModeOpenWrt
}
func (f fakeHost) Verify(context.Context, *http.Request) hostauth.Result { return f.res }

// newTestService 使用内存中的密码哈希存储，避免依赖数据库。
func newTestService(h HostVerifier, hash string) *Service {
	s := New(nil, h)
	s.getHash = func(context.Context) string { return hash }
	s.setHash = func(_ context.Context, v string, onlyIfAbsent bool) (bool, error) {
		if onlyIfAbsent && hash != "" {
			return false, nil
		}
		hash = v
		return true, nil
	}
	return s
}

func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName && c.Value != "" {
			return c
		}
	}
	return nil
}

func TestStatusModes(t *testing.T) {
	ctx := context.Background()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	okHost := hostauth.Result{OK: true, User: "admin"}
	failHost := hostauth.Result{Reason: hostauth.ReasonNoCookie}

	cases := []struct {
		name     string
		host     fakeHost
		hash     string
		mode     string
		authed   bool
		hint     string
		authzOK  bool
		authzErr string
	}{
		{"docker no pw", fakeHost{hostauth.ModeDocker, okHost}, "", ModeSetupRequired, false, "", false, "setup_required"},
		{"binary no pw", fakeHost{hostauth.ModeBinary, okHost}, "", ModeSetupRequired, false, "", false, "setup_required"},
		{"syno uninit trusted", fakeHost{hostauth.ModeSynology, okHost}, "", ModeSetupRequired, false, "", false, "setup_required"},
		{"owrt uninit trusted zero-pw", fakeHost{hostauth.ModeOpenWrt, okHost}, "", ModeDevice, true, "", true, ""},
		{"owrt uninit untrusted blocked", fakeHost{hostauth.ModeOpenWrt, failHost}, "", ModeHostLoginRequired, false, hostauth.ReasonNoCookie, false, "setup_required"},
		{"syno uninit untrusted blocked", fakeHost{hostauth.ModeSynology, failHost}, "", ModeHostLoginRequired, false, hostauth.ReasonNoCookie, false, "setup_required"},
		{"fnos uninit trusted gateway zero-pw", fakeHost{hostauth.ModeFnOS, okHost}, "", ModeDevice, true, "", true, ""},
		{"fnos uninit untrusted tcp blocked", fakeHost{hostauth.ModeFnOS, hostauth.Result{Reason: hostauth.ReasonNoFnOSGateway}}, "", ModeHostLoginRequired, false, hostauth.ReasonNoFnOSGateway, false, "setup_required"},
		{"pw set, trusted host auto login", fakeHost{hostauth.ModeSynology, okHost}, hashPassword("secret1"), ModeDevice, true, "", true, ""},
		{"pw set, untrusted browser needs pw", fakeHost{hostauth.ModeSynology, failHost}, hashPassword("secret1"), ModePassword, false, "", false, "unauthorized"},
		{"pw set docker", fakeHost{hostauth.ModeDocker, failHost}, hashPassword("secret1"), ModePassword, false, "", false, "unauthorized"},
	}
	for _, c := range cases {
		s := newTestService(c.host, c.hash)
		st := s.Status(ctx, req)
		if st.Mode != c.mode || st.Authenticated != c.authed || st.HintCode != c.hint || st.InstallMode != c.host.mode {
			t.Errorf("%s: status=%+v", c.name, st)
		}
		ok, code := s.Authorize(ctx, req)
		if ok != c.authzOK || code != c.authzErr {
			t.Errorf("%s: authorize=%v,%q", c.name, ok, code)
		}
	}
}

func TestSetupAndLogin(t *testing.T) {
	ctx := context.Background()
	s := newTestService(fakeHost{mode: hostauth.ModeDocker}, "")

	// 未设置密码时 login 返回 setup_required
	if err := s.Login(ctx, httptest.NewRecorder(), "x"); !errors.Is(err, ErrSetupRequired) {
		t.Fatalf("login before setup: %v", err)
	}
	// 未设置密码时不允许通过 SetPassword 无旧密码设置
	if err := s.SetPassword(ctx, httptest.NewRecorder(), "", "newpass1"); !errors.Is(err, ErrSetupRequired) {
		t.Fatalf("set password before setup: %v", err)
	}
	if err := s.Setup(ctx, httptest.NewRecorder(), "123"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("short: %v", err)
	}

	rec := httptest.NewRecorder()
	if err := s.Setup(ctx, rec, "secret1"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := sessionCookie(rec)
	if c == nil {
		t.Fatal("setup should issue session cookie")
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	if st := s.Status(ctx, req); st.Mode != ModePassword || !st.Authenticated {
		t.Fatalf("after setup: %+v", st)
	}
	if ok, _ := s.Authorize(ctx, req); !ok {
		t.Fatal("authorize after setup")
	}

	// 再次 setup 被拒绝
	if err := s.Setup(ctx, httptest.NewRecorder(), "another1"); !errors.Is(err, ErrPasswordSet) {
		t.Fatalf("second setup: %v", err)
	}

	// 登录
	if err := s.Login(ctx, httptest.NewRecorder(), "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong pw: %v", err)
	}
	if err := s.Login(ctx, httptest.NewRecorder(), "secret1"); err != nil {
		t.Fatalf("login: %v", err)
	}

	// 修改密码：旧密码错误 / 新密码过短 / 成功后旧会话失效
	if err := s.SetPassword(ctx, httptest.NewRecorder(), "bad", "newpass1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("change wrong old: %v", err)
	}
	if err := s.SetPassword(ctx, httptest.NewRecorder(), "secret1", ""); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("change empty: %v", err)
	}
	rec2 := httptest.NewRecorder()
	if err := s.SetPassword(ctx, rec2, "secret1", "newpass1"); err != nil {
		t.Fatalf("change: %v", err)
	}
	if ok, _ := s.Authorize(ctx, req); ok {
		t.Fatal("old session should be invalidated after password change")
	}
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(sessionCookie(rec2))
	if ok, _ := s.Authorize(ctx, req2); !ok {
		t.Fatal("new session should be valid")
	}
}

func TestOpenWrtDeviceTicketSingleUseHandoff(t *testing.T) {
	ctx := context.Background()
	s := newTestService(fakeHost{mode: hostauth.ModeOpenWrt, res: hostauth.Result{Reason: hostauth.ReasonNoCookie}}, "")

	tok := s.IssueDeviceTicket()
	if tok == "" {
		t.Fatal("expected non-empty ticket")
	}

	rec := httptest.NewRecorder()
	if !s.ConsumeDeviceTicket(rec, tok) {
		t.Fatal("first consume should succeed")
	}
	// Single-use: consuming same ticket again must fail
	if s.ConsumeDeviceTicket(httptest.NewRecorder(), tok) {
		t.Fatal("reusing consumed ticket must fail")
	}

	var devCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == deviceCookieName && c.Value != "" {
			devCookie = c
		}
	}
	if devCookie == nil {
		t.Fatal("expected device session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	req.AddCookie(devCookie)
	st := s.Status(ctx, req)
	if st.Mode != ModeDevice || !st.Authenticated {
		t.Fatalf("expected ModeDevice authenticated=true after handoff, got %+v", st)
	}
	if ok, _ := s.Authorize(ctx, req); !ok {
		t.Fatal("expected Authorize=true with device session cookie")
	}
}

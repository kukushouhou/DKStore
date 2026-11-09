package hostauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeFS struct {
	files map[string]string
}

func (f fakeFS) Exists(p string) bool { _, ok := f.files[p]; return ok }
func (f fakeFS) ReadFile(p string) ([]byte, error) {
	if s, ok := f.files[p]; ok {
		return []byte(s), nil
	}
	return nil, errors.New("not found")
}

func TestDetectInstallMode(t *testing.T) {
	t.Setenv("CONTAINER", "")
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"dockerenv", map[string]string{"/.dockerenv": "", "/var/packages/dkstore": ""}, ModeDocker},
		{"cgroup docker", map[string]string{"/proc/1/cgroup": "0::/docker/abc", "/etc/openwrt_release": ""}, ModeDocker},
		{"cgroup containerd", map[string]string{"/proc/1/cgroup": "0::/system.slice/containerd.service/x"}, ModeDocker},
		{"synology", map[string]string{"/var/packages/dkstore": "", "/proc/1/cgroup": "0::/"}, ModeSynology},
		{"openwrt", map[string]string{"/etc/openwrt_release": ""}, ModeOpenWrt},
		{"binary", map[string]string{}, ModeBinary},
	}
	for _, c := range cases {
		if got := DetectInstallMode(fakeFS{c.files}); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestValidToken(t *testing.T) {
	ok := []string{"abc", "A-b_c.9", "0123456789abcdef0123456789abcdef"}
	bad := []string{"", "a b", "a;b", "a'b", `a"b`, "a$(id)", "a\nb", "中文", "a}b", strings.Repeat("a", 257)}
	for _, s := range ok {
		if !ValidToken(s) {
			t.Errorf("expected valid: %q", s)
		}
	}
	for _, s := range bad {
		if ValidToken(s) {
			t.Errorf("expected invalid: %q", s)
		}
	}
}

type call struct {
	env  []string
	name string
	args []string
}

type fakeRunner struct {
	calls []call
	fn    func(name string, args []string, env []string) ([]byte, error)
}

func (f *fakeRunner) run(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{env, name, args})
	return f.fn(name, args, env)
}

func reqWithCookies(cookies map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/status", nil)
	r.RemoteAddr = "192.168.1.10:51234"
	for k, v := range cookies {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	return r
}

func rawCookieReq(h string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.168.1.10:51234"
	r.Header.Set("Cookie", h)
	return r
}

func synoFS() fakeFS {
	return fakeFS{files: map[string]string{synoAuthCGI: ""}}
}

func TestSynologyVerify(t *testing.T) {
	fr := &fakeRunner{fn: func(name string, args []string, env []string) ([]byte, error) {
		switch name {
		case synoAuthCGI:
			for _, e := range env {
				if e == "HTTP_COOKIE=id=goodsid" {
					return []byte("admin\n"), nil
				}
				if e == "HTTP_COOKIE=id=usersid" {
					return []byte("bob\n"), nil
				}
			}
			return nil, errors.New("exit 1")
		case synoIDBin:
			if args[1] == "admin" {
				return []byte("users administrators\n"), nil
			}
			return []byte("users\n"), nil
		}
		return nil, errors.New("unexpected")
	}}
	v := New(ModeSynology, fr.run, synoFS())

	if res := v.Verify(context.Background(), reqWithCookies(nil)); res.OK || res.Reason != ReasonNoCookie {
		t.Fatalf("no cookie: %+v", res)
	}
	if res := v.Verify(context.Background(), rawCookieReq("id=bad%id")); res.OK || res.Reason != ReasonInvalidCookie {
		t.Fatalf("invalid cookie: %+v", res)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("invalid cookie must not exec, got %d calls", len(fr.calls))
	}

	res := v.Verify(context.Background(), reqWithCookies(map[string]string{"id": "goodsid"}))
	if !res.OK || res.User != "admin" {
		t.Fatalf("good: %+v", res)
	}
	// 检查环境变量
	env := strings.Join(fr.calls[0].env, "\n")
	if !strings.Contains(env, "REMOTE_ADDR=192.168.1.10") {
		t.Errorf("missing REMOTE_ADDR: %s", env)
	}

	if res := v.Verify(context.Background(), reqWithCookies(map[string]string{"id": "usersid"})); res.OK || res.Reason != ReasonNotAdmin {
		t.Fatalf("non admin: %+v", res)
	}
	if res := v.Verify(context.Background(), reqWithCookies(map[string]string{"id": "expired"})); res.OK || res.Reason != ReasonSessionInvalid {
		t.Fatalf("expired: %+v", res)
	}
}

func TestSynologyTokenPassed(t *testing.T) {
	var gotEnv []string
	fr := &fakeRunner{fn: func(name string, args []string, env []string) ([]byte, error) {
		if name == synoAuthCGI {
			gotEnv = env
			return []byte("admin"), nil
		}
		return []byte("administrators"), nil
	}}
	v := New(ModeSynology, fr.run, synoFS())
	r := reqWithCookies(map[string]string{"id": "sid1"})
	r.Header.Set("X-SYNO-TOKEN", "tok123")
	if res := v.Verify(context.Background(), r); !res.OK {
		t.Fatalf("expected ok: %+v", res)
	}
	if !strings.Contains(strings.Join(gotEnv, "\n"), "HTTP_X_SYNO_TOKEN=tok123") {
		t.Errorf("token not passed: %v", gotEnv)
	}
	r2 := reqWithCookies(map[string]string{"id": "sid1"})
	r2.Header.Set("X-SYNO-TOKEN", "tok 123")
	if res := v.Verify(context.Background(), r2); res.OK || res.Reason != ReasonInvalidCookie {
		t.Fatalf("bad token: %+v", res)
	}
}

func TestSynologyUnavailable(t *testing.T) {
	fr := &fakeRunner{fn: func(string, []string, []string) ([]byte, error) { return []byte("admin"), nil }}
	v := New(ModeSynology, fr.run, fakeFS{files: map[string]string{}})
	if res := v.Verify(context.Background(), reqWithCookies(map[string]string{"id": "sid"})); res.OK || res.Reason != ReasonUnavailable {
		t.Fatalf("unavailable: %+v", res)
	}
}

func TestSynologyAdminFallbackGroupFile(t *testing.T) {
	fr := &fakeRunner{fn: func(name string, args []string, env []string) ([]byte, error) {
		if name == synoAuthCGI {
			return []byte("alice\n"), nil
		}
		return nil, errors.New("no id binary")
	}}
	fs := fakeFS{files: map[string]string{
		synoAuthCGI:  "",
		"/etc/group": "users:x:100:\nadministrators:x:101:admin,alice\n",
	}}
	v := New(ModeSynology, fr.run, fs)
	if res := v.Verify(context.Background(), reqWithCookies(map[string]string{"id": "sid"})); !res.OK {
		t.Fatalf("group fallback: %+v", res)
	}
}

func TestOpenWrtVerify(t *testing.T) {
	fr := &fakeRunner{fn: func(name string, args []string, env []string) ([]byte, error) {
		if name != "/bin/ubus" || len(args) != 4 || args[0] != "call" || args[1] != "session" || args[2] != "get" {
			return nil, errors.New("unexpected")
		}
		switch args[3] {
		case `{"ubus_rpc_session":"rootsid"}`:
			return []byte(`{"values":{"username":"root","token":"x"}}`), nil
		case `{"ubus_rpc_session":"usersid"}`:
			return []byte(`{"values":{"username":"guest"}}`), nil
		}
		return nil, errors.New("Command failed: Not found")
	}}
	fs := fakeFS{files: map[string]string{"/bin/ubus": "", "/www/luci-static": ""}}
	v := New(ModeOpenWrt, fr.run, fs)
	ctx := context.Background()

	for _, name := range []string{"sysauth", "sysauth_http", "sysauth_https"} {
		v2 := New(ModeOpenWrt, fr.run, fs)
		if res := v2.Verify(ctx, reqWithCookies(map[string]string{name: "rootsid"})); !res.OK {
			t.Fatalf("%s: %+v", name, res)
		}
	}
	if res := v.Verify(ctx, reqWithCookies(map[string]string{"sysauth": "usersid"})); res.OK || res.Reason != ReasonNotAdmin {
		t.Fatalf("guest: %+v", res)
	}
	if res := v.Verify(ctx, reqWithCookies(map[string]string{"sysauth": "nosuch"})); res.OK || res.Reason != ReasonSessionInvalid {
		t.Fatalf("invalid: %+v", res)
	}
	n := len(fr.calls)
	if res := v.Verify(ctx, rawCookieReq("sysauth=x}{(id)")); res.OK || res.Reason != ReasonInvalidCookie {
		t.Fatalf("injection: %+v", res)
	}
	if len(fr.calls) != n {
		t.Fatal("invalid sid must not exec")
	}
	if res := v.Verify(ctx, reqWithCookies(nil)); res.OK || res.Reason != ReasonNoCookie {
		t.Fatalf("no cookie: %+v", res)
	}
}

func TestOpenWrtUnavailable(t *testing.T) {
	fr := &fakeRunner{fn: func(string, []string, []string) ([]byte, error) { return nil, errors.New("x") }}
	// 有 ubus 无 LuCI
	v := New(ModeOpenWrt, fr.run, fakeFS{files: map[string]string{"/bin/ubus": ""}})
	if res := v.Verify(context.Background(), reqWithCookies(map[string]string{"sysauth": "sid"})); res.Reason != ReasonUnavailable {
		t.Fatalf("no luci: %+v", res)
	}
	// 无 ubus
	v = New(ModeOpenWrt, fr.run, fakeFS{files: map[string]string{"/www/luci-static": ""}})
	if res := v.Verify(context.Background(), reqWithCookies(map[string]string{"sysauth": "sid"})); res.Reason != ReasonUnavailable {
		t.Fatalf("no ubus: %+v", res)
	}
	if len(fr.calls) != 0 {
		t.Fatal("should not exec when unavailable")
	}
}

func TestCache(t *testing.T) {
	fr := &fakeRunner{fn: func(string, []string, []string) ([]byte, error) {
		return []byte(`{"values":{"username":"root"}}`), nil
	}}
	fs := fakeFS{files: map[string]string{"/bin/ubus": "", "/www/luci-static": ""}}
	v := New(ModeOpenWrt, fr.run, fs)
	now := time.Unix(1000, 0)
	v.now = func() time.Time { return now }
	r := func() *http.Request { return reqWithCookies(map[string]string{"sysauth": "sid"}) }

	v.Verify(context.Background(), r())
	v.Verify(context.Background(), r())
	if len(fr.calls) != 1 {
		t.Fatalf("expected cached, calls=%d", len(fr.calls))
	}
	now = now.Add(61 * time.Second)
	v.Verify(context.Background(), r())
	if len(fr.calls) != 2 {
		t.Fatalf("expected refresh after ttl, calls=%d", len(fr.calls))
	}
}

func TestUnsupportedMode(t *testing.T) {
	for _, m := range []string{ModeDocker, ModeBinary} {
		v := New(m, nil, fakeFS{})
		if v.Supported() {
			t.Errorf("%s should not be supported", m)
		}
		if res := v.Verify(context.Background(), reqWithCookies(map[string]string{"id": "x", "sysauth": "y"})); res.OK || res.Reason != ReasonUnsupportedMode {
			t.Errorf("%s: %+v", m, res)
		}
	}
}

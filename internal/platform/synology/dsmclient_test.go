package synology

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
)

// fakeDSM 是用于单元测试的简易 DSM WebAPI 模拟器。
type fakeDSM struct {
	mu        sync.Mutex
	logins    int32
	logouts   int32
	sidSeq    int
	validSID  string
	loginWait time.Duration

	// handler 处理除登录/注销外的 API；返回 (data, errCode)。errCode != 0 表示失败。
	handler func(api, method string, form map[string]string, sid string) (any, int)
	// rawHandler 若返回 true 表示已自行写出响应
	rawHandler func(w http.ResponseWriter, api, method string) bool

	loginQueryHadPasswd bool
	loginMethod         string
}

func (f *fakeDSM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("passwd") != "" {
		f.mu.Lock()
		f.loginQueryHadPasswd = true
		f.mu.Unlock()
	}
	_ = r.ParseForm()
	form := map[string]string{}
	for k := range r.PostForm {
		form[k] = r.PostForm.Get(k)
	}
	api, method := form["api"], form["method"]
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	if api == "SYNO.API.Auth" && method == "login" {
		if f.loginWait > 0 {
			time.Sleep(f.loginWait)
		}
		atomic.AddInt32(&f.logins, 1)
		f.mu.Lock()
		f.loginMethod = r.Method
		f.sidSeq++
		f.validSID = "sid" + string(rune('0'+f.sidSeq))
		sid := f.validSID
		f.mu.Unlock()
		write(map[string]any{"success": true, "data": map[string]any{"sid": sid}})
		return
	}
	if api == "SYNO.API.Auth" && method == "logout" {
		atomic.AddInt32(&f.logouts, 1)
		write(map[string]any{"success": true})
		return
	}
	if f.rawHandler != nil && f.rawHandler(w, api, method) {
		return
	}
	data, code := f.handler(api, method, form, form["_sid"])
	if code != 0 {
		write(map[string]any{"success": false, "error": map[string]any{"code": code}})
		return
	}
	write(map[string]any{"success": true, "data": data})
}

func newTestClient(t *testing.T, f *fakeDSM) (*dsmClient, func()) {
	t.Helper()
	srv := httptest.NewServer(f)
	c := newDSMClient(DSMConfig{URL: srv.URL, Username: "admin", Secret: "p@ss"})
	return c, srv.Close
}

func TestDSMReloginOnSessionErrorCodes(t *testing.T) {
	for _, code := range []int{106, 107, 119} {
		f := &fakeDSM{}
		calls := 0
		f.handler = func(api, method string, form map[string]string, sid string) (any, int) {
			calls++
			if calls == 1 {
				return nil, code
			}
			return map[string]any{}, 0
		}
		c, done := newTestClient(t, f)
		if _, err := c.ListProjects(context.Background()); err != nil {
			t.Fatalf("code %d: expected relogin success, got %v", code, err)
		}
		if got := atomic.LoadInt32(&f.logins); got != 2 {
			t.Fatalf("code %d: expected 2 logins, got %d", code, got)
		}
		done()
	}
}

func TestDSMNoReloginOnOtherErrors(t *testing.T) {
	f := &fakeDSM{}
	f.handler = func(api, method string, form map[string]string, sid string) (any, int) { return nil, 105 }
	c, done := newTestClient(t, f)
	defer done()
	_, err := c.ListProjects(context.Background())
	var de *DSMError
	if !errors.As(err, &de) || de.Code != 105 {
		t.Fatalf("expected DSMError 105, got %v", err)
	}
	if got := atomic.LoadInt32(&f.logins); got != 1 {
		t.Fatalf("expected 1 login, got %d", got)
	}
	if en := i18n.LocalizeErr(i18n.EnUS, err); !strings.Contains(en, "error code 105") {
		t.Fatalf("unexpected english message: %s", en)
	}
}

func TestDSMLoginUsesPOSTBody(t *testing.T) {
	f := &fakeDSM{handler: func(string, string, map[string]string, string) (any, int) { return map[string]any{}, 0 }}
	c, done := newTestClient(t, f)
	defer done()
	if _, _, err := c.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.loginQueryHadPasswd || f.loginMethod != http.MethodPost {
		t.Fatalf("password must be posted in body (method=%s, inQuery=%v)", f.loginMethod, f.loginQueryHadPasswd)
	}
}

func TestDSMConcurrentLoginMerged(t *testing.T) {
	f := &fakeDSM{loginWait: 150 * time.Millisecond}
	f.handler = func(string, string, map[string]string, string) (any, int) { return map[string]any{}, 0 }
	c, done := newTestClient(t, f)
	defer done()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.ListProjects(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&f.logins); got != 1 {
		t.Fatalf("expected concurrent logins merged into 1, got %d", got)
	}
}

func TestDSMLogoutOnClose(t *testing.T) {
	f := &fakeDSM{handler: func(string, string, map[string]string, string) (any, int) { return map[string]any{}, 0 }}
	srv := httptest.NewServer(f)
	defer srv.Close()
	ad := New("h1", nil, DSMConfig{URL: srv.URL, Username: "admin", Secret: "x"})
	if err := ad.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = ad.Close()
	if got := atomic.LoadInt32(&f.logouts); got != 1 {
		t.Fatalf("expected logout on Close, got %d", got)
	}
}

func TestDSMNeedOTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"error":{"code":403}}`))
	}))
	defer srv.Close()
	c := newDSMClient(DSMConfig{URL: srv.URL, Username: "a", Secret: "b"})
	_, _, err := c.Login(context.Background())
	var ae *DSMAuthError
	if !errors.As(err, &ae) || !ae.NeedOTP {
		t.Fatalf("expected NeedOTP auth error, got %v", err)
	}
	if !strings.Contains(ae.Localize(i18n.EnUS), "OTP") {
		t.Fatalf("unexpected message %q", ae.Localize(i18n.EnUS))
	}
}

func TestUpdateAndBuildProjectBothBuildsFail(t *testing.T) {
	f := &fakeDSM{}
	f.handler = func(api, method string, form map[string]string, sid string) (any, int) {
		switch method {
		case "update":
			return map[string]any{}, 0
		case "build_stream", "build":
			return nil, 1200
		}
		return map[string]any{}, 0
	}
	c, done := newTestClient(t, f)
	defer done()
	err := c.UpdateAndBuildProject(context.Background(), "p1", "services: {}")
	var pbe *ProjectBuildError
	if !errors.As(err, &pbe) || !pbe.YAMLWritten {
		t.Fatalf("expected ProjectBuildError with YAMLWritten, got %v", err)
	}
}

func TestTaskFailureDetection(t *testing.T) {
	cases := []struct {
		in   map[string]any
		fail bool
	}{
		{map[string]any{"finished": false}, false},
		{map[string]any{"finished": true, "error": ""}, false},
		{map[string]any{"error": "manifest unknown"}, true},
		{map[string]any{"status": "failed"}, true},
		{map[string]any{"error": map[string]any{"code": float64(0)}}, false},
		{map[string]any{"error": map[string]any{"code": float64(1001)}}, true},
	}
	for i, c := range cases {
		if got := taskFailure(c.in) != ""; got != c.fail {
			t.Fatalf("case %d: expected fail=%v, got %v", i, c.fail, got)
		}
	}
}

func TestDetectArch(t *testing.T) {
	cases := map[string]map[string]any{
		"amd64": {"cpu_vendor": "INTEL", "cpu_family": "Celeron"},
		"arm64": {"cpu_vendor": "Realtek", "cpu_series": "RTD1619B"},
		"":      {"model": "DS920+"},
	}
	for want, in := range cases {
		if got := detectArch(in); got != want {
			t.Fatalf("detectArch(%v) = %q, want %q", in, got, want)
		}
	}
	if got := detectArch(map[string]any{"arch": "aarch64"}); got != "arm64" {
		t.Fatalf("explicit arch: got %q", got)
	}
}

// projectFake 模拟 Project 更新流程。
type projectFake struct {
	mu            sync.Mutex
	yaml          string
	buildFails    bool
	switchImage   bool
	updates       []string
	containerImg  string
	pulledImageID string
}

func (p *projectFake) handle(api, method string, form map[string]string, sid string) (any, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch api + "." + method {
	case "SYNO.Docker.Image.pull_start":
		return map[string]any{}, 0
	case "SYNO.Docker.Image.list":
		return map[string]any{"images": []any{
			map[string]any{"id": p.pulledImageID, "repository": "nginx", "tags": []string{"1.27"}},
			map[string]any{"id": "sha256:old", "repository": "nginx", "tags": []string{"1.26"}},
		}}, 0
	case "SYNO.Docker.Project.get":
		return map[string]any{
			"id": "p1", "name": "web", "path": "/volume1/docker/web", "content": p.yaml,
			"containers": []any{map[string]any{
				"Id": "c1", "Name": "/web-nginx-1", "Image": p.containerImg,
				"Config": map[string]any{"Image": "nginx", "Labels": map[string]string{"com.docker.compose.service": "nginx"}},
				"State":  map[string]any{"Status": "running", "Running": true},
			}},
		}, 0
	case "SYNO.Docker.Project.update":
		var content string
		_ = json.Unmarshal([]byte(form["content"]), &content)
		p.updates = append(p.updates, content)
		p.yaml = content
		return map[string]any{}, 0
	case "SYNO.Docker.Project.build_stream", "SYNO.Docker.Project.build":
		if p.buildFails && strings.Contains(p.yaml, "1.27") {
			return nil, 1200
		}
		if p.switchImage {
			if strings.Contains(p.yaml, "1.27") {
				p.containerImg = p.pulledImageID
			} else {
				p.containerImg = "sha256:old"
			}
		}
		return map[string]any{}, 0
	}
	return map[string]any{}, 0
}

const origYAML = "services:\n  nginx:\n    image: nginx:1.26\n"

func runProjectUpdate(t *testing.T, p *projectFake) (*platform.UpdateResult, error) {
	t.Helper()
	oldTimeout, oldInterval := verifyTimeout, verifyInterval
	verifyTimeout, verifyInterval = 200*time.Millisecond, 20*time.Millisecond
	defer func() { verifyTimeout, verifyInterval = oldTimeout, oldInterval }()

	f := &fakeDSM{handler: p.handle}
	srv := httptest.NewServer(f)
	defer srv.Close()
	ad := New("h1", nil, DSMConfig{URL: srv.URL, Username: "admin", Secret: "x"})
	defer ad.Close()
	return ad.UpdateService(context.Background(), platform.UpdateRequest{
		AppName:        "web",
		ServiceName:    "nginx",
		Source:         platform.SourceDSMProject,
		Project:        &platform.ProjectInfo{Name: "web", DSMProjectID: "p1"},
		OldImageRef:    "nginx:1.26",
		TargetImageRef: "nginx:1.27",
	}, nil)
}

func TestProjectUpdateRollbackOnBuildFailure(t *testing.T) {
	p := &projectFake{yaml: origYAML, buildFails: true, containerImg: "sha256:old", pulledImageID: "sha256:new"}
	_, err := runProjectUpdate(t, p)
	var ue *platform.UpdateError
	if !errors.As(err, &ue) || ue.Code != "recreate_failed" {
		t.Fatalf("expected recreate_failed, got %v", err)
	}
	if len(p.updates) != 2 || p.updates[1] != origYAML {
		t.Fatalf("expected original YAML restored, updates=%q", p.updates)
	}
}

func TestProjectUpdateVerifyFailedRestores(t *testing.T) {
	p := &projectFake{yaml: origYAML, containerImg: "sha256:old", pulledImageID: "sha256:new"}
	_, err := runProjectUpdate(t, p)
	var ue *platform.UpdateError
	if !errors.As(err, &ue) || ue.Code != "verify_failed" {
		t.Fatalf("expected verify_failed, got %v", err)
	}
	if p.yaml != origYAML {
		t.Fatalf("expected original YAML restored, got %q", p.yaml)
	}
}

func TestProjectUpdateSuccess(t *testing.T) {
	p := &projectFake{yaml: origYAML, switchImage: true, containerImg: "sha256:old", pulledImageID: "sha256:new"}
	res, err := runProjectUpdate(t, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.NewImageID != "sha256:new" || !strings.Contains(p.yaml, "nginx:1.27") {
		t.Fatalf("unexpected result %+v yaml=%q", res, p.yaml)
	}
}

func TestStandaloneTagChangeUpgradeSuccess(t *testing.T) {
	oldTimeout, oldInterval, oldPoll := verifyTimeout, verifyInterval, taskPollInterval
	verifyTimeout, verifyInterval, taskPollInterval = 200*time.Millisecond, 20*time.Millisecond, 10*time.Millisecond
	defer func() { verifyTimeout, verifyInterval, taskPollInterval = oldTimeout, oldInterval, oldPoll }()

	var (
		mu             sync.Mutex
		calls          []string
		running        = true
		deleted        = false
		curImg         = "nginx:1.26"
		curImageID     = "sha256:old"
		createdProfile map[string]any
		createdRun     string
	)
	f := &fakeDSM{handler: func(api, method string, form map[string]string, sid string) (any, int) {
		mu.Lock()
		defer mu.Unlock()
		key := api + "." + method
		calls = append(calls, key)
		switch key {
		case "SYNO.Docker.Container.list":
			if deleted {
				return map[string]any{"containers": []any{}}, 0
			}
			return map[string]any{"containers": []any{map[string]any{
				"id": "c1", "name": "nginx", "Image": curImg, "ImageID": curImageID,
				"status": "running",
				"State":  map[string]any{"Running": running, "Status": "running"},
			}}}, 0
		case "SYNO.Docker.Container.get":
			return map[string]any{
				"profile": map[string]any{
					"name":  "nginx",
					"image": "nginx:1.26",
					"port_bindings": []any{
						map[string]any{"container_port": 80, "host_port": 8080, "type": "tcp_udp"},
						map[string]any{"container_port": 443, "host_port": 8443, "type": "tcp"},
					},
				},
				"details": map[string]any{
					"State": map[string]any{"Running": true},
				},
			}, 0
		case "SYNO.Docker.Image.pull_start":
			if !running || deleted {
				t.Errorf("old container must remain running during pull (running=%v, deleted=%v)", running, deleted)
			}
			return map[string]any{"task_id": "pull-1"}, 0
		case "SYNO.Docker.Image.pull_status":
			return map[string]any{"finished": true, "downloaded": 1.0}, 0
		case "SYNO.Docker.Image.list":
			return map[string]any{"images": []any{
				map[string]any{"id": "sha256:new", "repository": "nginx", "tags": []string{"1.27"}},
				map[string]any{"id": "sha256:old", "repository": "nginx", "tags": []string{"1.26"}},
			}}, 0
		case "SYNO.Docker.Container.stop":
			running = false
			return map[string]any{}, 0
		case "SYNO.Docker.Container.delete":
			if form["force"] != "false" || form["preserve_profile"] != "false" {
				t.Errorf("unexpected delete params: %v", form)
			}
			deleted = true
			return map[string]any{}, 0
		case "SYNO.Docker.Container.create":
			createdRun = form["is_run_instantly"]
			_ = json.Unmarshal([]byte(form["profile"]), &createdProfile)
			deleted = false
			running = createdRun == "true"
			curImg, _ = createdProfile["image"].(string)
			curImageID = "sha256:new"
			return map[string]any{}, 0
		}
		return map[string]any{}, 0
	}}

	srv := httptest.NewServer(f)
	defer srv.Close()
	ad := New("h1", nil, DSMConfig{URL: srv.URL, Username: "admin", Secret: "x"})
	defer ad.Close()

	res, err := ad.UpdateService(context.Background(), platform.UpdateRequest{
		AppName: "nginx", ServiceName: "nginx", Source: platform.SourceStandalone,
		OldImageRef: "nginx:1.26", TargetImageRef: "nginx:1.27",
	}, nil)
	if err != nil {
		t.Fatalf("expected upgrade success, got %v", err)
	}
	if res.NewImageID != "sha256:new" || createdRun != "true" || curImg != "nginx:1.27" {
		t.Fatalf("unexpected result=%+v createdRun=%q curImg=%q", res, createdRun, curImg)
	}
	// 验证 tcp_udp 端口绑定被拆分为 tcp + udp 两条，加上原有 443/tcp 共 3 条
	pbs, _ := createdProfile["port_bindings"].([]any)
	if len(pbs) != 3 {
		t.Fatalf("expected 3 port_bindings after tcp_udp split, got %v", pbs)
	}
	wantSeq := []string{
		"SYNO.Docker.Container.list",
		"SYNO.Docker.Container.get",
		"SYNO.Docker.Image.pull_start",
		"SYNO.Docker.Image.pull_status",
		"SYNO.Docker.Image.list",
		"SYNO.Docker.Container.stop",
		"SYNO.Docker.Container.delete",
		"SYNO.Docker.Container.create",
		"SYNO.Docker.Container.list",
	}
	if strings.Join(calls, ",") != strings.Join(wantSeq, ",") {
		t.Fatalf("unexpected call order:\ngot:  %v\nwant: %v", calls, wantSeq)
	}
}

func TestStandaloneTagChangeCreateFailsRestoresBackup(t *testing.T) {
	oldTimeout, oldInterval, oldPoll := verifyTimeout, verifyInterval, taskPollInterval
	verifyTimeout, verifyInterval, taskPollInterval = 200*time.Millisecond, 20*time.Millisecond, 10*time.Millisecond
	defer func() { verifyTimeout, verifyInterval, taskPollInterval = oldTimeout, oldInterval, oldPoll }()

	var (
		mu             sync.Mutex
		createdImages  []string
		restoredRunArg string
	)
	f := &fakeDSM{handler: func(api, method string, form map[string]string, sid string) (any, int) {
		mu.Lock()
		defer mu.Unlock()
		switch api + "." + method {
		case "SYNO.Docker.Container.list":
			return map[string]any{"containers": []any{map[string]any{
				"id": "c1", "name": "nginx", "Image": "nginx:1.26", "ImageID": "sha256:old",
				"State": map[string]any{"Running": true, "Status": "running"},
			}}}, 0
		case "SYNO.Docker.Container.get":
			return map[string]any{
				"profile": map[string]any{"name": "nginx", "image": "nginx:1.26"},
				"details": map[string]any{"State": map[string]any{"Running": true}},
			}, 0
		case "SYNO.Docker.Image.pull_start":
			return map[string]any{}, 0
		case "SYNO.Docker.Image.list":
			return map[string]any{"images": []any{map[string]any{"id": "sha256:new", "repository": "nginx", "tags": []string{"1.27"}}}}, 0
		case "SYNO.Docker.Container.stop", "SYNO.Docker.Container.delete":
			return map[string]any{}, 0
		case "SYNO.Docker.Container.create":
			var prof map[string]any
			_ = json.Unmarshal([]byte(form["profile"]), &prof)
			img, _ := prof["image"].(string)
			createdImages = append(createdImages, img)
			if img == "nginx:1.27" {
				return nil, 1300 // 模拟新配置创建失败
			}
			restoredRunArg = form["is_run_instantly"]
			return map[string]any{}, 0
		}
		return map[string]any{}, 0
	}}

	srv := httptest.NewServer(f)
	defer srv.Close()
	ad := New("h1", nil, DSMConfig{URL: srv.URL, Username: "admin", Secret: "x"})
	defer ad.Close()

	_, err := ad.UpdateService(context.Background(), platform.UpdateRequest{
		AppName: "nginx", ServiceName: "nginx", Source: platform.SourceStandalone,
		OldImageRef: "nginx:1.26", TargetImageRef: "nginx:1.27",
	}, nil)
	var ue *platform.UpdateError
	if !errors.As(err, &ue) || ue.Code != "recreate_failed" {
		t.Fatalf("expected recreate_failed, got %v", err)
	}
	if len(createdImages) != 2 || createdImages[0] != "nginx:1.27" || createdImages[1] != "nginx:1.26" || restoredRunArg != "true" {
		t.Fatalf("expected backup profile restore with nginx:1.26 (run=true), got images=%v run=%q", createdImages, restoredRunArg)
	}
}

func TestCLIComposeContainerRejectedAndClassified(t *testing.T) {
	pulled := false
	stopped := false
	f := &fakeDSM{handler: func(api, method string, form map[string]string, sid string) (any, int) {
		switch api + "." + method {
		case "SYNO.Docker.Project.list":
			return map[string]any{}, 0
		case "SYNO.Docker.Container.list":
			return map[string]any{"containers": []any{
				map[string]any{
					"id":      "c1",
					"name":    "mycli-web-1",
					"Image":   "nginx:1.26",
					"ImageID": "sha256:old",
					"status":  "running",
					"Labels": map[string]string{
						"com.docker.compose.project": "mycli",
						"com.docker.compose.service": "web",
					},
				},
			}}, 0
		case "SYNO.Docker.Image.list":
			return map[string]any{"images": []any{}}, 0
		case "SYNO.Docker.Image.pull_start":
			pulled = true
		case "SYNO.Docker.Container.stop":
			stopped = true
		}
		return map[string]any{}, 0
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	ad := New("h1", nil, DSMConfig{URL: srv.URL, Username: "admin", Secret: "x"})
	defer ad.Close()

	// 1) ListApps 应将未登记在 DSM Project 中但带有 com.docker.compose.project 标签的容器归类为 SourceCompose
	apps, err := ad.ListApps(context.Background())
	if err != nil {
		t.Fatalf("ListApps failed: %v", err)
	}
	if len(apps) != 1 || apps[0].Source != platform.SourceCompose || apps[0].Name != "mycli" {
		t.Fatalf("expected CLI compose app classified as SourceCompose, got %+v", apps)
	}
	if apps[0].Project == nil || apps[0].Project.DSMProjectID != "" {
		t.Fatalf("expected empty DSMProjectID for CLI compose app, got %+v", apps[0].Project)
	}

	// 2) UpdateService 跨版本更新该命令行容器时应直接返回 dsm_cli_compose_unsupported 通俗提示，且不拉取、不停容器
	_, err = ad.UpdateService(context.Background(), platform.UpdateRequest{
		AppName: "mycli", ServiceName: "web", Source: platform.SourceCompose,
		Project: apps[0].Project, OldImageRef: "nginx:1.26", TargetImageRef: "nginx:1.27",
	}, nil)
	var ue *platform.UpdateError
	if !errors.As(err, &ue) || ue.Code != "dsm_cli_compose_unsupported" {
		t.Fatalf("expected dsm_cli_compose_unsupported, got %v", err)
	}
	if !strings.Contains(ue.Message, "SSH") {
		t.Fatalf("expected SSH hint in error message, got %q", ue.Message)
	}
	if pulled || stopped {
		t.Fatalf("must not pull or stop CLI compose container (pulled=%v, stopped=%v)", pulled, stopped)
	}

	// 3) 即使调用方传了 SourceStandalone 且列表未带标签，只要 GetContainerProfile 读出 com.docker.compose.project 标签也应拦截
	f2 := &fakeDSM{handler: func(api, method string, form map[string]string, sid string) (any, int) {
		switch api + "." + method {
		case "SYNO.Docker.Container.list":
			return map[string]any{"containers": []any{map[string]any{"id": "c2", "name": "cli-only", "Image": "nginx:1.26", "ImageID": "sha256:old"}}}, 0
		case "SYNO.Docker.Container.get":
			return map[string]any{
				"profile": map[string]any{
					"name":   "cli-only",
					"image":  "nginx:1.26",
					"labels": map[string]any{"com.docker.compose.project": "cli-proj"},
				},
				"details": map[string]any{"State": map[string]any{"Running": true}},
			}, 0
		case "SYNO.Docker.Image.pull_start":
			pulled = true
		}
		return map[string]any{}, 0
	}}
	srv2 := httptest.NewServer(f2)
	defer srv2.Close()
	ad2 := New("h2", nil, DSMConfig{URL: srv2.URL, Username: "admin", Secret: "x"})
	defer ad2.Close()
	_, err = ad2.UpdateService(context.Background(), platform.UpdateRequest{
		AppName: "cli-only", ServiceName: "cli-only", Source: platform.SourceStandalone,
		OldImageRef: "nginx:1.26", TargetImageRef: "nginx:1.27",
	}, nil)
	if !errors.As(err, &ue) || ue.Code != "dsm_cli_compose_unsupported" || pulled {
		t.Fatalf("expected profile compose label rejected without pull, err=%v pulled=%v", err, pulled)
	}
}

func TestStandaloneSameTagVerifiesImageChange(t *testing.T) {
	oldTimeout, oldInterval, oldPoll := verifyTimeout, verifyInterval, taskPollInterval
	verifyTimeout, verifyInterval, taskPollInterval = 200*time.Millisecond, 20*time.Millisecond, 10*time.Millisecond
	defer func() { verifyTimeout, verifyInterval, taskPollInterval = oldTimeout, oldInterval, oldPoll }()

	for _, upgraded := range []bool{true, false} {
		imageID := "sha256:old"
		f := &fakeDSM{handler: func(api, method string, form map[string]string, sid string) (any, int) {
			switch api + "." + method {
			case "SYNO.Docker.Container.list":
				return map[string]any{"containers": []any{map[string]any{"id": "c1", "name": "nginx", "Image": "nginx:latest", "ImageID": imageID}}}, 0
			case "SYNO.Docker.Image.list":
				return map[string]any{"images": []any{map[string]any{"id": "sha256:new", "repository": "library/nginx", "tags": []string{"latest"}}}}, 0
			case "SYNO.Docker.Image.upgrade_start":
				if upgraded {
					imageID = "sha256:new"
				}
			}
			return map[string]any{}, 0
		}}
		srv := httptest.NewServer(f)
		ad := New("h1", nil, DSMConfig{URL: srv.URL, Username: "admin", Secret: "x"})
		res, err := ad.UpdateService(context.Background(), platform.UpdateRequest{
			AppName: "nginx", ServiceName: "nginx", Source: platform.SourceStandalone,
			OldImageRef: "nginx:latest", TargetImageRef: "docker.io/library/nginx:latest",
		}, nil)
		if upgraded {
			if err != nil || res.NewImageID != "sha256:new" {
				t.Fatalf("expected success, got res=%+v err=%v", res, err)
			}
		} else {
			var ue *platform.UpdateError
			if !errors.As(err, &ue) || ue.Code != "verify_failed" {
				t.Fatalf("expected verify_failed when image unchanged, got %v", err)
			}
		}
		_ = ad.Close()
		srv.Close()
	}
}

func TestPullImage_SplitsThirdPartyRegistryAndAuth(t *testing.T) {
	var gotForm map[string]string
	f := &fakeDSM{handler: func(api, method string, form map[string]string, sid string) (any, int) {
		if api == "SYNO.Docker.Image" && method == "pull_start" {
			gotForm = form
			return map[string]any{"task_id": ""}, 0
		}
		return map[string]any{}, 0
	}}
	c, done := newTestClient(t, f)
	defer done()

	if err := c.PullImage(context.Background(), "ghcr.io/kukushouhou/card-bill-assistant", "v1.2.0", "ghuser", "ghtoken", nil); err != nil {
		t.Fatalf("PullImage ghcr.io failed: %v", err)
	}
	if gotForm["registry"] != `"https://ghcr.io"` || gotForm["repository"] != `"kukushouhou/card-bill-assistant"` || gotForm["tag"] != `"v1.2.0"` {
		t.Fatalf("unexpected ghcr pull_start params: %v", gotForm)
	}
	if gotForm["username"] != `"ghuser"` || gotForm["password"] != `"ghtoken"` {
		t.Fatalf("expected username/password passed to pull_start, got %v", gotForm)
	}

	if err := c.PullImage(context.Background(), "docker.io/library/nginx", "1.27", "", "", nil); err != nil {
		t.Fatalf("PullImage docker.io failed: %v", err)
	}
	if gotForm["registry"] != `""` || gotForm["repository"] != `"library/nginx"` {
		t.Fatalf("expected empty registry and stripped docker.io prefix, got %v", gotForm)
	}
}

func TestDSMProjectUpdate_GHCRDirectBuildStreamWithLayerStreaming(t *testing.T) {
	oldTimeout, oldInterval := verifyTimeout, verifyInterval
	verifyTimeout, verifyInterval = 200*time.Millisecond, 20*time.Millisecond
	defer func() { verifyTimeout, verifyInterval = oldTimeout, oldInterval }()

	pullStartCalled := false
	curYAML := "services:\n  app:\n    image: ghcr.io/kukushouhou/card-bill-assistant:v1.1.0\n"
	curImgID := "sha256:old111"

	f := &fakeDSM{}
	f.rawHandler = func(w http.ResponseWriter, api, method string) bool {
		if api == "SYNO.Docker.Project" && method == "build_stream" {
			fl, _ := w.(http.Flusher)
			chunks := []string{
				" app Pulling \r\n",
				" 6f837d52a1b9 Downloading [=====>     ]  15.0MB/30.0MB\r",
				" \x1b[32m✔\x1b[0m 6f837d52a1b9 Pull complete\r\n",
				" Container card-bill-assistant-1  Recreated\r\n",
				" Container card-bill-assistant-1  Started\r\n",
			}
			for _, ch := range chunks {
				_, _ = w.Write([]byte(ch))
				if fl != nil {
					fl.Flush()
				}
			}
			curImgID = "sha256:new222"
			return true
		}
		return false
	}
	f.handler = func(api, method string, form map[string]string, sid string) (any, int) {
		switch api + "." + method {
		case "SYNO.Docker.Image.pull_start":
			pullStartCalled = true
			return nil, 1202
		case "SYNO.Docker.Project.get":
			return map[string]any{
				"id": "p-ghcr", "name": "card-bill-assistant", "path": "/volume1/docker/card-bill-assistant", "content": curYAML,
				"containers": []any{map[string]any{
					"Id": "c1", "Name": "/card-bill-assistant-1", "Image": curImgID,
					"Config": map[string]any{"Image": "ghcr.io/kukushouhou/card-bill-assistant:v1.1.0", "Labels": map[string]string{"com.docker.compose.service": "app"}},
					"State":  map[string]any{"Status": "running", "Running": true},
				}},
			}, 0
		case "SYNO.Docker.Project.update":
			_ = json.Unmarshal([]byte(form["content"]), &curYAML)
			return map[string]any{}, 0
		case "SYNO.Docker.Image.list":
			return map[string]any{"images": []any{
				map[string]any{"id": "sha256:new222", "repository": "ghcr.io/kukushouhou/card-bill-assistant", "tags": []string{"v1.2.0"}},
			}}, 0
		}
		return map[string]any{}, 0
	}

	srv := httptest.NewServer(f)
	defer srv.Close()
	ad := New("h1", nil, DSMConfig{URL: srv.URL, Username: "admin", Secret: "x"})
	defer ad.Close()

	authB64 := base64.URLEncoding.EncodeToString([]byte(`{"username":"u","password":"p"}`))
	var events []platform.PullProgress
	res, err := ad.UpdateService(context.Background(), platform.UpdateRequest{
		AppName:         "card-bill-assistant",
		ServiceName:     "app",
		Source:          platform.SourceDSMProject,
		Project:         &platform.ProjectInfo{Name: "card-bill-assistant", DSMProjectID: "p-ghcr"},
		OldImageRef:     "ghcr.io/kukushouhou/card-bill-assistant:v1.1.0",
		TargetImageRef:  "ghcr.io/kukushouhou/card-bill-assistant:v1.2.0",
		RegistryAuthB64: authB64,
	}, func(p platform.PullProgress) {
		events = append(events, p)
	})
	if err != nil {
		t.Fatalf("UpdateService ghcr DSM project failed: %v", err)
	}
	if pullStartCalled {
		t.Fatal("DSM Project tag update must go directly to build_stream without calling SYNO.Docker.Image.pull_start")
	}
	if res.NewImageID != "sha256:new222" {
		t.Fatalf("expected NewImageID=sha256:new222, got %s", res.NewImageID)
	}
	sawLayer := false
	sawANSIChunk := false
	for _, ev := range events {
		if len(ev.Layers) > 0 && ev.Layers[0].ID == "6f837d52a1b9" {
			sawLayer = true
		}
		if strings.Contains(ev.LogChunk, "\x1b[32m✔\x1b[0m") {
			sawANSIChunk = true
		}
	}
	if !sawLayer || !sawANSIChunk {
		t.Fatalf("expected live layer progress (%v) and raw ANSI LogChunk (%v)", sawLayer, sawANSIChunk)
	}
}

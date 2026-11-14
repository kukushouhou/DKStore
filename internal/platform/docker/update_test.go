package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/transport"
)

func TestReplaceServiceImageInYAML_PreservesCommentsAndSiblingDatabase(t *testing.T) {
	orig := `# Custom compose file with comments
version: "3.9"
services:
  # Database service must NEVER be modified
  db:
    image: postgres:15.4 # keep pinned postgres
    restart: always
    volumes:
      - ./pgdata:/var/lib/postgresql/data
  app:
    # Main application service
    image: ghcr.io/immich-app/immich-server:v1.98.0 # inline comment
    depends_on:
      - db
    ports:
      - "2283:3001"
`
	updated, changed := replaceServiceImageInYAML([]byte(orig), "app", "ghcr.io/immich-app/immich-server:v1.99.0")
	if !changed {
		t.Fatalf("expected changed=true")
	}
	got := string(updated)
	if !strings.Contains(got, "image: postgres:15.4 # keep pinned postgres") {
		t.Fatalf("database service image was unexpectedly altered:\n%s", got)
	}
	if !strings.Contains(got, "image: ghcr.io/immich-app/immich-server:v1.99.0 # inline comment") {
		t.Fatalf("target service image was not updated or inline comment lost:\n%s", got)
	}
	if !strings.Contains(got, "# Custom compose file with comments") {
		t.Fatalf("top comment lost:\n%s", got)
	}
}

func TestUpdateProjectCompose_RestoresOriginalOnFailure(t *testing.T) {
	origYAML := "services:\n  web:\n    image: nginx:1.26.0\n"
	newYAML := "services:\n  web:\n    image: nginx:1.27.3\n"
	var calls []bool
	tr := &fakeExecTransport{
		files: map[string][]byte{
			"/opt/app/compose.yaml": []byte(origYAML),
		},
		composeProjHandler: func(_ string, pullAndRemoveOrphans bool) (string, error) {
			calls = append(calls, pullAndRemoveOrphans)
			if pullAndRemoveOrphans {
				return "pull failed", errors.New("compose up failed")
			}
			return "restored", nil
		},
	}
	ad := New("h1", tr)
	proj := &platform.ProjectInfo{
		Name:        "app",
		WorkingDir:  "/opt/app",
		ConfigFiles: []string{"compose.yaml"},
	}
	err := ad.UpdateProjectCompose(context.Background(), "app", proj, newYAML, nil)
	if err == nil {
		t.Fatal("expected UpdateProjectCompose error on compose failure")
	}
	if string(tr.files["/opt/app/compose.yaml"]) != origYAML {
		t.Fatalf("expected original compose.yaml to be restored, got:\n%s", string(tr.files["/opt/app/compose.yaml"]))
	}
	if len(calls) != 2 || !calls[0] || calls[1] {
		t.Fatalf("expected [pull+up=true, restore_up=false], got %v", calls)
	}
}

type fakeExecTransport struct {
	doHandler          func(path string) (*http.Response, error)
	reqHandler         func(method, path string, body []byte) (*http.Response, error)
	composeProjHandler func(workDir string, pullAndRemoveOrphans bool) (string, error)
	files              map[string][]byte
}

func (f *fakeExecTransport) Mode() transport.Mode { return transport.ModeLocal }
func (f *fakeExecTransport) Close() error         { return nil }
func (f *fakeExecTransport) DoDocker(_ context.Context, path string) (*http.Response, error) {
	if f.doHandler == nil {
		return nil, io.EOF
	}
	return f.doHandler(path)
}
func (f *fakeExecTransport) ReadFile(_ context.Context, path string) ([]byte, error) {
	if b, ok := f.files[path]; ok {
		return b, nil
	}
	return nil, io.EOF
}
func (f *fakeExecTransport) RequestDocker(_ context.Context, method, path string, body io.Reader, _ map[string]string) (*http.Response, error) {
	var b []byte
	if body != nil {
		b, _ = io.ReadAll(body)
	}
	return f.reqHandler(method, path, b)
}
func (f *fakeExecTransport) WriteFileAtomic(_ context.Context, path string, content []byte, _ string) error {
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.files[path] = content
	return nil
}
func (f *fakeExecTransport) ExecComposeService(_ context.Context, _ string, _, _ []string, _, _ string, _ bool) (string, error) {
	return "compose ok", nil
}
func (f *fakeExecTransport) ExecComposeProject(_ context.Context, workDir string, _, _ []string, _ string, pullAndRemoveOrphans bool) (string, error) {
	if f.composeProjHandler != nil {
		return f.composeProjHandler(workDir, pullAndRemoveOrphans)
	}
	return "compose project ok", nil
}

var _ transport.ExecutableTransport = (*fakeExecTransport)(nil)

func jsonHTTPResp(status int, payload any) *http.Response {
	b, _ := json.Marshal(payload)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(b)),
	}
}

func TestPullImageStream_LayerProgress(t *testing.T) {
	streamLines := strings.Join([]string{
		`{"status":"Pulling from library/nginx","id":"1.27"}`,
		`{"status":"Pulling fs layer","id":"a1b2c3d4e5f6"}`,
		`{"status":"Pulling fs layer","id":"b2c3d4e5f6a1"}`,
		`{"status":"Downloading","id":"a1b2c3d4e5f6","progressDetail":{"current":500,"total":1000}}`,
		`{"status":"Download complete","id":"a1b2c3d4e5f6"}`,
		`{"status":"Pull complete","id":"a1b2c3d4e5f6"}`,
		`{"status":"Already exists","id":"b2c3d4e5f6a1"}`,
		`{"status":"Digest: sha256:1111111111111111111111111111111111111111111111111111111111111111"}`,
	}, "\n")

	tr := &fakeExecTransport{
		doHandler: func(path string) (*http.Response, error) {
			return jsonHTTPResp(200, map[string]any{
				"Id":          "sha256:newimg1234567890",
				"RepoDigests": []string{"nginx@sha256:1111111111111111111111111111111111111111111111111111111111111111"},
			}), nil
		},
		reqHandler: func(method, path string, _ []byte) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(streamLines)),
			}, nil
		},
	}
	ad := New("local", tr)
	var events []platform.PullProgress
	imgID, digest, err := ad.pullImageStream(context.Background(), tr, "nginx:1.27", "", func(p platform.PullProgress) {
		events = append(events, p)
	})
	if err != nil {
		t.Fatalf("pullImageStream failed: %v", err)
	}
	if imgID != "sha256:newimg1234567890" {
		t.Fatalf("unexpected imgID: %s", imgID)
	}
	if !strings.HasPrefix(digest, "sha256:111111") {
		t.Fatalf("unexpected digest: %s", digest)
	}
	if len(events) == 0 {
		t.Fatalf("expected progress events")
	}
	last := events[len(events)-1]
	if last.TotalLayers != 2 || last.CompletedLayers != 2 {
		t.Fatalf("expected 2/2 layers completed, got %+v", last)
	}
	if len(last.Layers) != 2 || last.Layers[0].ID != "a1b2c3d4e5f6" || last.Layers[0].Status != "done" || last.Layers[1].Status != "done" {
		t.Fatalf("expected ordered Layers slice with done status, got %+v", last.Layers)
	}
	var allChunks strings.Builder
	for _, ev := range events {
		allChunks.WriteString(ev.LogChunk)
	}
	if !strings.Contains(allChunks.String(), "a1b2c3d4e5f6") || !strings.Contains(allChunks.String(), "\r\n") {
		t.Fatalf("expected formatted terminal LogChunk output, got %q", allChunks.String())
	}
}

func TestRecreateContainerViaEngine_StoppedContainerStaysStopped(t *testing.T) {
	var calls []string
	var createdBody map[string]any

	tr := &fakeExecTransport{
		reqHandler: func(method, path string, body []byte) (*http.Response, error) {
			calls = append(calls, method+" "+path)
			if strings.HasPrefix(path, "/containers/create") {
				_ = json.Unmarshal(body, &createdBody)
				return jsonHTTPResp(201, map[string]any{"Id": "newcid999"}), nil
			}
			return jsonHTTPResp(204, nil), nil
		},
	}
	ad := New("local", tr)
	rawInspect := map[string]any{
		"Name": "/my-stopped-tool",
		"Config": map[string]any{
			"Image":  "busybox:1.35",
			"Env":    []string{"FOO=bar"},
			"Labels": map[string]any{"custom.label": "val"},
		},
		"HostConfig": map[string]any{
			"Binds": []string{"/host/data:/data"},
		},
		"NetworkSettings": map[string]any{
			"Networks": map[string]any{
				"bridge": map[string]any{
					"Aliases": []string{"tool-alias"},
				},
			},
		},
	}

	newID, err := ad.recreateContainerViaEngine(context.Background(), tr, "oldcid111", rawInspect, "busybox:1.36", false)
	if err != nil {
		t.Fatalf("recreateContainerViaEngine failed: %v", err)
	}
	if newID != "newcid999" {
		t.Fatalf("expected newcid999, got %s", newID)
	}
	for _, c := range calls {
		if strings.Contains(c, "/start") || strings.Contains(c, "/stop") {
			t.Fatalf("stopped container must NOT be stopped or started during recreation, got call: %s (all: %v)", c, calls)
		}
	}
	if createdBody["Image"] != "busybox:1.36" {
		t.Fatalf("expected Image=busybox:1.36 in create payload, got %v", createdBody["Image"])
	}
}

func TestRecreateContainerViaEngine_RestoresOldContainerOnStartFailure(t *testing.T) {
	var calls []string
	tr := &fakeExecTransport{
		reqHandler: func(method, path string, _ []byte) (*http.Response, error) {
			calls = append(calls, method+" "+path)
			if strings.HasPrefix(path, "/containers/create") {
				return jsonHTTPResp(201, map[string]any{"Id": "newcid_broken"}), nil
			}
			if path == "/containers/newcid_broken/start" {
				return jsonHTTPResp(500, map[string]any{"message": "port conflict"}), nil
			}
			return jsonHTTPResp(204, nil), nil
		},
	}
	ad := New("local", tr)
	rawInspect := map[string]any{
		"Name":   "/my-running-app",
		"Config": map[string]any{"Image": "app:1.0"},
	}
	_, err := ad.recreateContainerViaEngine(context.Background(), tr, "oldcid111", rawInspect, "app:2.0", true)
	if err == nil {
		t.Fatalf("expected error when new container fails to start")
	}
	joined := strings.Join(calls, " | ")
	if !strings.Contains(joined, "DELETE /containers/newcid_broken?force=1") {
		t.Fatalf("expected broken new container to be deleted, calls: %s", joined)
	}
	if !strings.Contains(joined, "POST /containers/oldcid111/rename?name=my-running-app") {
		t.Fatalf("expected old container name to be restored, calls: %s", joined)
	}
	if !strings.Contains(joined, "POST /containers/oldcid111/start") {
		t.Fatalf("expected old running container to be restarted, calls: %s", joined)
	}
}

func TestUpdateService_DetectsExternalConflict(t *testing.T) {
	tr := &fakeExecTransport{
		doHandler: func(path string) (*http.Response, error) {
			if strings.HasPrefix(path, "/containers/json") {
				return jsonHTTPResp(200, []map[string]any{
					{"Id": "cid123", "Names": []string{"/web"}},
				}), nil
			}
			return jsonHTTPResp(200, map[string]any{
				"Id":    "cid123",
				"Name":  "/web",
				"Image": "sha256:actual_different_image_id",
				"State": map[string]any{"Running": true, "Status": "running"},
			}), nil
		},
	}
	ad := New("local", tr)
	_, err := ad.UpdateService(context.Background(), platform.UpdateRequest{
		Source:          platform.SourceStandalone,
		AppName:         "web",
		ServiceName:     "web",
		ExpectedImageID: "sha256:expected_old_image_id",
		TargetImageRef:  "nginx:1.27",
	}, nil)
	if err == nil {
		t.Fatalf("expected conflict error")
	}
	var ue *platform.UpdateError
	if !errors.As(err, &ue) || ue.Code != "external_conflict" {
		t.Fatalf("expected external_conflict UpdateError, got: %v", err)
	}
}

func TestStandaloneConfig_ReadDiffsImageEnvAndUpdateOverridesPorts(t *testing.T) {
	var createdBody map[string]any
	curImgID := "sha256:oldimg1"
	tr := &fakeExecTransport{
		doHandler: func(path string) (*http.Response, error) {
			switch {
			case strings.HasPrefix(path, "/containers/json"):
				return jsonHTTPResp(200, []map[string]any{
					{"Id": "cid-sa", "Names": []string{"/jellyfin"}},
				}), nil
			case strings.HasPrefix(path, "/containers/cid-sa/json"), strings.HasPrefix(path, "/containers/cid-new/json"):
				return jsonHTTPResp(200, map[string]any{
					"Id":    "cid-sa",
					"Name":  "/jellyfin",
					"Image": curImgID,
					"State": map[string]any{"Running": true, "Status": "running"},
					"Config": map[string]any{
						"Image": "jellyfin/jellyfin:10.9.0",
						"Env":   []string{"PATH=/usr/local/bin:/usr/bin", "TZ=Asia/Shanghai", "PUID=1000"},
					},
					"HostConfig": map[string]any{
						"PortBindings": map[string]any{
							"8096/tcp": []map[string]string{{"HostIp": "0.0.0.0", "HostPort": "8096"}},
						},
					},
					"Mounts": []map[string]any{
						{"Source": "/srv/media", "Destination": "/media", "RW": false},
					},
				}), nil
			case strings.Contains(path, "oldimg1"):
				return jsonHTTPResp(200, map[string]any{
					"Id":     "sha256:oldimg1",
					"Config": map[string]any{"Env": []string{"PATH=/usr/local/bin:/usr/bin"}},
				}), nil
			default:
				return jsonHTTPResp(200, map[string]any{
					"Id":          "sha256:newimg2",
					"RepoDigests": []string{"jellyfin/jellyfin@sha256:2222222222222222222222222222222222222222222222222222222222222222"},
				}), nil
			}
		},
		reqHandler: func(method, path string, body []byte) (*http.Response, error) {
			if strings.HasPrefix(path, "/images/create") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(strings.NewReader(`{"status":"Pull complete","id":"112233445566"}` + "\n")),
				}, nil
			}
			if strings.HasPrefix(path, "/containers/create") {
				_ = json.Unmarshal(body, &createdBody)
				curImgID = "sha256:newimg2"
				return jsonHTTPResp(201, map[string]any{"Id": "cid-new"}), nil
			}
			return jsonHTTPResp(204, nil), nil
		},
	}

	ad := New("local", tr)
	cfg, err := ad.GetStandaloneConfig(context.Background(), "jellyfin", "main")
	if err != nil {
		t.Fatalf("GetStandaloneConfig failed: %v", err)
	}
	if !cfg.Editable || len(cfg.Env) != 2 || cfg.Env[0].Key != "TZ" {
		t.Fatalf("expected image default PATH stripped and 2 user env vars returned, got %+v", cfg)
	}
	if len(cfg.Ports) != 1 || cfg.Ports[0].ContainerPort != "8096" || len(cfg.Volumes) != 1 || cfg.Volumes[0].Mode != "ro" {
		t.Fatalf("unexpected ports/volumes: %+v", cfg)
	}

	res, err := ad.UpdateStandaloneConfig(context.Background(), platform.StandaloneUpdateRequest{
		AppName:         "jellyfin",
		ServiceName:     "main",
		ExpectedImageID: "sha256:oldimg1",
		OldImageRef:     "jellyfin/jellyfin:10.9.0",
		TargetImageRef:  "jellyfin/jellyfin:10.10.0",
		Env:             []platform.EnvVar{{Key: "TZ", Value: "UTC"}},
		Ports:           []platform.PortBinding{{HostIP: "127.0.0.1", HostPort: "18096", ContainerPort: "8096", Protocol: "tcp"}},
	}, nil)
	if err != nil || res.NewImageID != "sha256:newimg2" {
		t.Fatalf("UpdateStandaloneConfig failed: res=%+v err=%v", res, err)
	}
	hc, _ := createdBody["HostConfig"].(map[string]any)
	pbs, _ := hc["PortBindings"].(map[string]any)
	if pbs == nil || pbs["8096/tcp"] == nil {
		t.Fatalf("expected overridden PortBindings in create payload, got %+v", createdBody)
	}
}

type fakeOptionsExecTransport struct {
	fakeExecTransport
	recordedPull          []bool
	recordedRemoveOrphans []bool
}

func (f *fakeOptionsExecTransport) ExecComposeProjectWithOptions(
	_ context.Context,
	workDir string,
	_, _ []string,
	_ string,
	pullImages, removeOrphans bool,
	_ func(string),
) (string, error) {
	f.recordedPull = append(f.recordedPull, pullImages)
	f.recordedRemoveOrphans = append(f.recordedRemoveOrphans, removeOrphans)
	if f.composeProjHandler != nil {
		return f.composeProjHandler(workDir, removeOrphans)
	}
	return "ok", nil
}

func TestUpdateProjectCompose_ServiceRemovalGuardAndMultiFileAndRepoSwap(t *testing.T) {
	origPrimary := "services:\n  web:\n    image: repo-a.example.com/app/web:1.0\n  db:\n    image: postgres:16-alpine\n"
	tr := &fakeOptionsExecTransport{
		fakeExecTransport: fakeExecTransport{
			files: map[string][]byte{
				"/opt/stack/compose.yaml": []byte(origPrimary),
			},
		},
	}
	ad := New("h1", tr)
	proj := &platform.ProjectInfo{
		Name:        "stack",
		WorkingDir:  "/opt/stack",
		ConfigFiles: []string{"compose.yaml"},
	}

	// 1. 未确认时遗漏 db：必须直接拒绝，不改写文件，不调用 compose 执行
	omittedDB := "services:\n  web:\n    image: repo-a.example.com/app/web:1.1\n"
	err := ad.UpdateProjectCompose(context.Background(), "stack", proj, omittedDB, nil)
	var csrErr *platform.ComposeServicesRemovedError
	if !errors.As(err, &csrErr) {
		t.Fatalf("expected ComposeServicesRemovedError when db is omitted without confirmation, got %v", err)
	}
	if len(csrErr.RemovedServices) != 1 || csrErr.RemovedServices[0] != "db" {
		t.Fatalf("expected removed=[db], got %+v", csrErr)
	}
	if string(tr.files["/opt/stack/compose.yaml"]) != origPrimary {
		t.Fatal("compose.yaml must NOT be overwritten when service removal is unconfirmed")
	}
	if len(tr.recordedPull) != 0 {
		t.Fatalf("compose execution must NOT run when service removal is unconfirmed, got %v", tr.recordedPull)
	}

	// 2. 同一服务仅更换镜像仓库地址（repo-a -> ghcr.io）：不属于服务减少，应正常更新且不传 --remove-orphans
	repoSwapped := "services:\n  web:\n    image: ghcr.io/app/web:1.1\n  db:\n    image: postgres:16-alpine\n"
	if err := ad.UpdateProjectCompose(context.Background(), "stack", proj, repoSwapped, nil); err != nil {
		t.Fatalf("image repo swap must not be blocked: %v", err)
	}
	if len(tr.recordedPull) != 1 || !tr.recordedPull[0] || tr.recordedRemoveOrphans[0] {
		t.Fatalf("normal update must run pull=true removeOrphans=false, got pull=%v orphans=%v", tr.recordedPull, tr.recordedRemoveOrphans)
	}

	// 3. 多 Compose 文件场景：db 定义在 compose.override.yaml 中，主文件不含 db 不应误判缺失
	tr.files["/opt/stack/compose.yaml"] = []byte("services:\n  web:\n    image: ghcr.io/app/web:1.0\n")
	tr.files["/opt/stack/compose.override.yaml"] = []byte("services:\n  db:\n    image: postgres:16-alpine\n")
	multiProj := &platform.ProjectInfo{
		Name:        "stack",
		WorkingDir:  "/opt/stack",
		ConfigFiles: []string{"compose.yaml", "compose.override.yaml"},
	}
	if err := ad.UpdateProjectCompose(context.Background(), "stack", multiProj, omittedDB, nil); err != nil {
		t.Fatalf("multi-file compose with db in override must not report db removed: %v", err)
	}

	// 4. 用户显式确认移除旧服务（ConfirmRemoveServices=true）：放行更新并传递 removeOrphans=true
	tr.files["/opt/stack/compose.yaml"] = []byte(origPrimary)
	tr.recordedPull = nil
	tr.recordedRemoveOrphans = nil
	if err := ad.UpdateProjectComposeWithOptions(context.Background(), "stack", proj, omittedDB, platform.ComposeUpdateOptions{
		ConfirmRemoveServices: true,
	}, nil); err != nil {
		t.Fatalf("confirmed service removal failed: %v", err)
	}
	if len(tr.recordedPull) != 1 || !tr.recordedPull[0] || !tr.recordedRemoveOrphans[0] {
		t.Fatalf("confirmed service removal must run pull=true removeOrphans=true, got pull=%v orphans=%v", tr.recordedPull, tr.recordedRemoveOrphans)
	}
}

func TestUpdateProjectCompose_ReportsRollbackRebuildError(t *testing.T) {
	origYAML := "services:\n  web:\n    image: nginx:1.26.0\n"
	newYAML := "services:\n  web:\n    image: nginx:1.27.3\n"
	tr := &fakeExecTransport{
		files: map[string][]byte{
			"/opt/app/compose.yaml": []byte(origYAML),
		},
		composeProjHandler: func(_ string, pullAndRemoveOrphans bool) (string, error) {
			if pullAndRemoveOrphans {
				return "", errors.New("new compose up failed")
			}
			return "", errors.New("rollback up also failed")
		},
	}
	ad := New("h1", tr)
	proj := &platform.ProjectInfo{
		Name:        "app",
		WorkingDir:  "/opt/app",
		ConfigFiles: []string{"compose.yaml"},
	}
	err := ad.UpdateProjectCompose(context.Background(), "app", proj, newYAML, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "compose_rebuild_err") && !strings.Contains(err.Error(), "rollback up also failed") {
		t.Fatalf("expected rollback rebuild failure to be surfaced, got %v", err)
	}
}

package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kukushouhou/DKStore/internal/platform"
)

type fakeCleanupTransport struct {
	fakeExecTransport
	composeDownCalled bool
	removedPaths      []string
}

func (f *fakeCleanupTransport) ExecComposeDown(_ context.Context, _ string, _, _ []string, _ string, _ bool) (string, error) {
	f.composeDownCalled = true
	return "Stopped & Removed", nil
}

func (f *fakeCleanupTransport) RemoveHostPaths(_ context.Context, paths []string) error {
	f.removedPaths = append(f.removedPaths, paths...)
	return nil
}

func jsonResp(status int, v any) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(b)),
		Header:     make(http.Header),
	}
}

func TestDeleteStandaloneContainer_ImagesAndProtectedSocket(t *testing.T) {
	var deletedReqs []string
	tr := &fakeCleanupTransport{
		fakeExecTransport: fakeExecTransport{
			doHandler: func(path string) (*http.Response, error) {
				switch path {
				case "/containers/json?all=1":
					return jsonResp(http.StatusOK, []map[string]any{
						{
							"Id":     "cid1234567890",
							"Names":  []string{"/portainer"},
							"Image":  "portainer/portainer-ce:sts",
							"State":  "running",
							"Labels": map[string]string{},
						},
					}), nil
				case "/containers/cid1234567890/json":
					return jsonResp(http.StatusOK, map[string]any{
						"Id":    "cid1234567890",
						"Name":  "/portainer",
						"Image": "sha256:aaa111",
						"Config": map[string]any{
							"Image": "portainer/portainer-ce:sts",
						},
						"Mounts": []any{
							map[string]any{
								"Type":        "volume",
								"Name":        "portainer_data",
								"Destination": "/data",
								"RW":          true,
							},
							map[string]any{
								"Type":        "bind",
								"Source":      "/var/run/docker.sock",
								"Destination": "/var/run/docker.sock",
								"RW":          true,
							},
						},
					}), nil
				default:
					return jsonResp(http.StatusNotFound, map[string]string{}), nil
				}
			},
			reqHandler: func(method, path string, _ []byte) (*http.Response, error) {
				deletedReqs = append(deletedReqs, method+" "+path)
				return jsonResp(http.StatusNoContent, nil), nil
			},
		},
	}

	ad := New("h1", tr)
	prev, err := ad.PreviewDeleteApp(context.Background(), "portainer", platform.SourceStandalone, nil)
	if err != nil {
		t.Fatalf("PreviewDeleteApp failed: %v", err)
	}
	if prev.IsProject || len(prev.Containers) != 1 || prev.Containers[0] != "portainer" {
		t.Fatalf("unexpected preview: %+v", prev)
	}
	if len(prev.Files) != 2 {
		t.Fatalf("expected 2 files (volume + protected socket), got %+v", prev.Files)
	}
	if prev.Files[0].ID != "volume:portainer_data" || !prev.Files[0].DefaultSelect || prev.Files[0].Protected {
		t.Fatalf("expected volume:portainer_data defaultSelect=true protected=false, got %+v", prev.Files[0])
	}
	if prev.Files[1].ID != "bind:/var/run/docker.sock" || !prev.Files[1].Protected {
		t.Fatalf("expected docker.sock to be protected, got %+v", prev.Files[1])
	}

	// 即使恶意传入受保护的 docker.sock ID，也绝不能被执行删除
	res, err := ad.DeleteApp(context.Background(), platform.DeleteAppRequest{
		AppName:      "portainer",
		Source:       platform.SourceStandalone,
		DeleteImages: true,
		FileIDs:      []string{"volume:portainer_data", "bind:/var/run/docker.sock"},
	})
	if err != nil {
		t.Fatalf("DeleteApp failed: %v", err)
	}
	if res.RemovedContainers != 1 || res.RemovedImages != 1 || res.RemovedFiles != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if len(tr.removedPaths) != 0 {
		t.Fatalf("protected socket must never reach RemoveHostPaths, got %v", tr.removedPaths)
	}
	joined := strings.Join(deletedReqs, " | ")
	if !strings.Contains(joined, "DELETE /containers/cid1234567890?force=1&v=0") ||
		!strings.Contains(joined, "DELETE /volumes/portainer_data?force=1") ||
		!strings.Contains(joined, "DELETE /images/portainer%2Fportainer-ce:sts?force=0") {
		t.Fatalf("unexpected docker engine delete calls: %s", joined)
	}
}

func TestDeleteComposeProject_PreservesUncheckedSubdirAndExternalBind(t *testing.T) {
	tr := &fakeCleanupTransport{
		fakeExecTransport: fakeExecTransport{
			files: map[string][]byte{
				"/vol1/1000/docker/immich/compose.yaml": []byte("services:\n  server:\n    image: ghcr.io/immich-app/immich-server:v1.131.3\n  redis:\n    image: redis:6.2-alpine\n"),
				"/vol1/1000/docker/immich/.env":         []byte("DB_PASSWORD=secret\n"),
			},
			doHandler: func(path string) (*http.Response, error) {
				switch path {
				case "/containers/json?all=1":
					return jsonResp(http.StatusOK, []map[string]any{
						{
							"Id":    "c_srv",
							"Names": []string{"/immich_server"},
							"Image": "ghcr.io/immich-app/immich-server:v1.131.3",
							"Labels": map[string]string{
								"com.docker.compose.project":             "immich",
								"com.docker.compose.service":             "server",
								"com.docker.compose.project.working_dir": "/vol1/1000/docker/immich",
							},
						},
						{
							"Id":    "c_redis",
							"Names": []string{"/immich_redis"},
							"Image": "redis:6.2-alpine",
							"Labels": map[string]string{
								"com.docker.compose.project": "immich",
								"com.docker.compose.service": "redis",
							},
						},
					}), nil
				case "/containers/c_srv/json":
					return jsonResp(http.StatusOK, map[string]any{
						"Id":   "c_srv",
						"Name": "/immich_server",
						"Config": map[string]any{
							"Image": "ghcr.io/immich-app/immich-server:v1.131.3",
						},
						"Mounts": []any{
							map[string]any{
								"Type":        "bind",
								"Source":      "/vol1/1000/docker/immich/pgdata",
								"Destination": "/var/lib/postgresql/data",
								"RW":          true,
							},
							map[string]any{
								"Type":        "bind",
								"Source":      "/vol1/1000/Photos",
								"Destination": "/usr/src/app/upload",
								"RW":          true,
							},
						},
					}), nil
				case "/containers/c_redis/json":
					return jsonResp(http.StatusOK, map[string]any{
						"Id":   "c_redis",
						"Name": "/immich_redis",
						"Config": map[string]any{
							"Image": "redis:6.2-alpine",
						},
					}), nil
				case "/networks":
					return jsonResp(http.StatusOK, []map[string]any{
						{
							"Id":     "net_immich",
							"Name":   "immich_default",
							"Labels": map[string]string{"com.docker.compose.project": "immich"},
						},
					}), nil
				default:
					return jsonResp(http.StatusNotFound, map[string]string{}), nil
				}
			},
			reqHandler: func(method, path string, _ []byte) (*http.Response, error) {
				if strings.Contains(path, "redis:6.2-alpine") && method == http.MethodDelete {
					// 模拟 redis 镜像被其他容器共用返回 409 Conflict
					return jsonResp(http.StatusConflict, map[string]string{"message": "image is being used"}), nil
				}
				return jsonResp(http.StatusNoContent, nil), nil
			},
		},
	}

	ad := New("h1", tr)
	proj := &platform.ProjectInfo{
		Name:        "immich",
		WorkingDir:  "/vol1/1000/docker/immich",
		ConfigFiles: []string{"compose.yaml"},
	}

	prev, err := ad.PreviewDeleteApp(context.Background(), "immich", platform.SourceCompose, proj)
	if err != nil {
		t.Fatalf("PreviewDeleteApp failed: %v", err)
	}
	if !prev.IsProject || len(prev.Containers) != 2 {
		t.Fatalf("expected project with 2 containers, got %+v", prev)
	}
	// 外部共享目录 /vol1/1000/Photos 默认不应自动选中
	var foundExternal bool
	for _, f := range prev.Files {
		if f.Path == "/vol1/1000/Photos" {
			foundExternal = true
			if f.DefaultSelect {
				t.Fatalf("external bind /vol1/1000/Photos must have DefaultSelect=false")
			}
		}
	}
	if !foundExternal {
		t.Fatalf("expected /vol1/1000/Photos in preview files: %+v", prev.Files)
	}

	// 用户只勾选了 project_dir，但取消了子目录 pgdata：应仅删除 compose.yaml 与 .env，不得整目录 rm -rf
	res, err := ad.DeleteApp(context.Background(), platform.DeleteAppRequest{
		AppName:      "immich",
		Source:       platform.SourceCompose,
		Project:      proj,
		DeleteImages: true,
		FileIDs:      []string{"project_dir:/vol1/1000/docker/immich"},
	})
	if err != nil {
		t.Fatalf("DeleteApp failed: %v", err)
	}
	if !tr.composeDownCalled {
		t.Fatal("expected ExecComposeDown to be called")
	}
	if res.RemovedContainers != 2 || res.RemovedImages != 1 || len(res.SkippedImages) != 1 {
		t.Fatalf("unexpected delete result: %+v", res)
	}
	for _, p := range tr.removedPaths {
		if p == "/vol1/1000/docker/immich" || p == "/vol1/1000/docker/immich/pgdata" {
			t.Fatalf("must not rm -rf project root when child pgdata is unchecked, got %v", tr.removedPaths)
		}
	}
	if len(tr.removedPaths) != 2 {
		t.Fatalf("expected compose.yaml and .env to be removed, got %v", tr.removedPaths)
	}
}

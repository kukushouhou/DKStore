package docker

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kukushouhou/DKStore/internal/platform"
)

type fakeLifecycleExecTransport struct {
	fakeOptionsExecTransport
	noStartCalled        bool
	recordedStartSvcs    []string
	recordedComposeAct   string
	recordedComposeSvcs  []string
}

func (f *fakeLifecycleExecTransport) ExecComposeProjectNoStart(
	_ context.Context,
	_ string,
	_, _ []string,
	_ string,
	_, _ bool,
	startServices []string,
	_ func(string),
) (string, error) {
	f.noStartCalled = true
	f.recordedStartSvcs = append([]string(nil), startServices...)
	return "recreated no-start", nil
}

func (f *fakeLifecycleExecTransport) ExecComposeAction(
	_ context.Context,
	_ string,
	_, _ []string,
	_ string,
	action string,
	services []string,
) (string, error) {
	f.recordedComposeAct = action
	f.recordedComposeSvcs = append([]string(nil), services...)
	return "ok", nil
}

func TestControlLifecycle_SingleContainerAndProject(t *testing.T) {
	var enginePaths []string
	tr := &fakeLifecycleExecTransport{
		fakeOptionsExecTransport: fakeOptionsExecTransport{
			fakeExecTransport: fakeExecTransport{
				files: map[string][]byte{
					"/opt/immich/compose.yaml": []byte("services:\n  server:\n    image: immich:v1\n"),
				},
				doHandler: func(path string) (*http.Response, error) {
					if path == "/containers/json?all=1" {
						return jsonHTTPResp(200, []map[string]any{
							{
								"Id":     "cid_ha_123456",
								"Names":  []string{"/home-assistant"},
								"State":  "exited",
								"Labels": map[string]string{},
							},
							{
								"Id":    "cid_im_srv",
								"Names": []string{"/immich-server-1"},
								"State": "running",
								"Labels": map[string]string{
									"com.docker.compose.project": "immich",
									"com.docker.compose.service": "server",
								},
							},
						}), nil
					}
					return jsonHTTPResp(404, nil), nil
				},
				reqHandler: func(method, path string, _ []byte) (*http.Response, error) {
					if method == http.MethodPost {
						enginePaths = append(enginePaths, path)
					}
					return jsonHTTPResp(204, nil), nil
				},
			},
		},
	}

	ad := New("h1", tr)

	// 1. 单容器启动
	if err := ad.ControlLifecycle(context.Background(), platform.LifecycleRequest{
		AppName:       "home-assistant",
		Source:        platform.SourceStandalone,
		ContainerName: "home-assistant",
		Action:        platform.ActionStart,
	}); err != nil {
		t.Fatalf("single container start failed: %v", err)
	}
	if len(enginePaths) != 1 || enginePaths[0] != "/containers/cid_ha_123456/start" {
		t.Fatalf("expected /containers/cid_ha_123456/start, got %v", enginePaths)
	}

	// 2. 整项目停止（优先走 Compose CLI）
	proj := &platform.ProjectInfo{
		Name:        "immich",
		WorkingDir:  "/opt/immich",
		ConfigFiles: []string{"compose.yaml"},
	}
	if err := ad.ControlLifecycle(context.Background(), platform.LifecycleRequest{
		AppName: "immich",
		Source:  platform.SourceCompose,
		Project: proj,
		Action:  platform.ActionStop,
	}); err != nil {
		t.Fatalf("project stop failed: %v", err)
	}
	if tr.recordedComposeAct != "stop" {
		t.Fatalf("expected compose stop action, got %q", tr.recordedComposeAct)
	}
}

func TestUpdateService_StoppedContainerHonorsStartAfter(t *testing.T) {
	running := false
	curImg := "sha256:oldha"
	var startedNew bool

	tr := &fakeExecTransport{
		doHandler: func(path string) (*http.Response, error) {
			switch {
			case path == "/containers/json?all=1":
				st := "exited"
				if running {
					st = "running"
				}
				return jsonHTTPResp(200, []map[string]any{
					{
						"Id":     "cid_ha",
						"Names":  []string{"/home-assistant"},
						"State":  st,
						"Labels": map[string]string{},
					},
				}), nil
			case path == "/containers/cid_ha/json" || path == "/containers/cid_ha_new/json":
				st := "exited"
				if running {
					st = "running"
				}
				return jsonHTTPResp(200, map[string]any{
					"Id":    "cid_ha_new",
					"Name":  "/home-assistant",
					"Image": curImg,
					"State": map[string]any{"Status": st, "Running": running},
					"Config": map[string]any{
						"Image": "homeassistant/home-assistant:latest",
					},
					"HostConfig": map[string]any{},
				}), nil
			case strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
				return jsonHTTPResp(200, map[string]any{
					"Id":          "sha256:newha",
					"RepoDigests": []string{"homeassistant/home-assistant@sha256:dig999"},
					"Config":      map[string]any{},
				}), nil
			}
			return jsonHTTPResp(404, nil), nil
		},
		reqHandler: func(method, path string, _ []byte) (*http.Response, error) {
			switch {
			case method == http.MethodPost && strings.HasPrefix(path, "/images/create"):
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(strings.NewReader(`{"status":"Pull complete"}` + "\n")),
				}, nil
			case method == http.MethodPost && strings.HasPrefix(path, "/containers/create"):
				curImg = "sha256:newha"
				return jsonHTTPResp(201, map[string]any{"Id": "cid_ha_new"}), nil
			case method == http.MethodPost && path == "/containers/cid_ha_new/start":
				startedNew = true
				running = true
				return jsonHTTPResp(204, nil), nil
			}
			return jsonHTTPResp(204, nil), nil
		},
	}

	ad := New("h1", tr)
	startTrue := true
	res, err := ad.UpdateService(context.Background(), platform.UpdateRequest{
		AppName:        "home-assistant",
		ServiceName:    "home-assistant",
		Source:         platform.SourceStandalone,
		OldImageRef:    "homeassistant/home-assistant:latest",
		TargetImageRef: "homeassistant/home-assistant:latest",
		StartAfter:     &startTrue,
	}, nil)
	if err != nil {
		t.Fatalf("UpdateService with StartAfter=true failed: %v", err)
	}
	if !startedNew || res.ContainerState != "running" {
		t.Fatalf("expected stopped container to be started when StartAfter=true, got startedNew=%v state=%s", startedNew, res.ContainerState)
	}
}

func TestUpdateProjectCompose_PreservePartialRunningState(t *testing.T) {
	origYAML := "services:\n  server:\n    image: immich:v1\n  ml:\n    image: immich-ml:v1\n"
	newYAML := "services:\n  server:\n    image: immich:v2\n  ml:\n    image: immich-ml:v2\n"
	tr := &fakeLifecycleExecTransport{
		fakeOptionsExecTransport: fakeOptionsExecTransport{
			fakeExecTransport: fakeExecTransport{
				files: map[string][]byte{
					"/opt/immich/compose.yaml": []byte(origYAML),
				},
				doHandler: func(path string) (*http.Response, error) {
					if path == "/containers/json?all=1" {
						return jsonHTTPResp(200, []map[string]any{
							{
								"Id":    "c1",
								"State": "running",
								"Labels": map[string]string{
									"com.docker.compose.project": "immich",
									"com.docker.compose.service": "server",
								},
							},
							{
								"Id":    "c2",
								"State": "exited",
								"Labels": map[string]string{
									"com.docker.compose.project": "immich",
									"com.docker.compose.service": "ml",
								},
							},
						}), nil
					}
					return jsonHTTPResp(200, nil), nil
				},
			},
		},
	}

	ad := New("h1", tr)
	proj := &platform.ProjectInfo{
		Name:        "immich",
		WorkingDir:  "/opt/immich",
		ConfigFiles: []string{"compose.yaml"},
	}
	startFalse := false
	if err := ad.UpdateProjectComposeWithOptions(context.Background(), "immich", proj, newYAML, platform.ComposeUpdateOptions{
		StartAfter: &startFalse,
	}, nil); err != nil {
		t.Fatalf("UpdateProjectComposeWithOptions StartAfter=false failed: %v", err)
	}
	if !tr.noStartCalled {
		t.Fatal("expected ExecComposeProjectNoStart to be called when StartAfter=false")
	}
	if len(tr.recordedStartSvcs) != 1 || tr.recordedStartSvcs[0] != "server" {
		t.Fatalf("expected only originally running service [server] to be restarted, got %v", tr.recordedStartSvcs)
	}
}

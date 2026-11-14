package synology

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kukushouhou/DKStore/internal/platform"
)

func TestParseSynoWebAPIProjects(t *testing.T) {
	raw := []byte(`[Line 295] Exec WebAPI:  api=SYNO.Docker.Project, version=1, method=list, param={}, runner=SYSTEM_ADMIN
{
   "data" : {
      "1f475e1a-9480-4d09-8fc2-a96af7f0a4ac" : {
         "containerIds" : [ "e0df1931b3d7f2fd9f09f3460d55e1fb49a01b785f3ddbd518e0d8170ecbe5a9" ],
         "id" : "1f475e1a-9480-4d09-8fc2-a96af7f0a4ac",
         "is_package" : false,
         "name" : "card-bill-assistant",
         "path" : "/volume1/docker/card-bill-assistant",
         "share_path" : "/docker/card-bill-assistant",
         "status" : "RUNNING"
      },
      "ec47b6ca-bc2e-42d1-b9db-09c332166025" : {
         "id" : "ec47b6ca-bc2e-42d1-b9db-09c332166025",
         "is_package" : true,
         "name" : "baiduapp",
         "path" : "/var/packages/baiduapp/target/app",
         "status" : "RUNNING"
      }
   },
   "success" : true
}`)
	projs, err := parseSynoWebAPIProjects(raw)
	if err != nil {
		t.Fatalf("parseSynoWebAPIProjects failed: %v", err)
	}
	if len(projs) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projs))
	}
}

func TestListAppsViaDSMAPI_NormalizesRepoTagAndImageIDDigest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		apiName := r.FormValue("api")
		method := r.FormValue("method")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch apiName + "." + method {
		case "SYNO.API.Auth.login":
			write(map[string]any{"success": true, "data": map[string]any{"sid": "sid-1"}})
		case "SYNO.Docker.Project.list":
			write(map[string]any{
				"success": true,
				"data": map[string]any{
					"proj-1": map[string]any{"id": "proj-1", "name": "iot", "path": "/volume1/docker/iot", "status": "RUNNING"},
				},
			})
		case "SYNO.Docker.Project.get":
			write(map[string]any{
				"success": true,
				"data": map[string]any{
					"id":      "proj-1",
					"name":    "iot",
					"path":    "/volume1/docker/iot",
					"content": "services:\n  mosquitto:\n    image: eclipse-mosquitto:2\n",
					"containers": []map[string]any{
						{
							"Id":    "c1",
							"Name":  "/iot-mosquitto-1",
							"Image": "sha256:imgid111",
							"State": map[string]any{"Status": "running"},
							"Config": map[string]any{
								"Image":  "eclipse-mosquitto:2",
								"Labels": map[string]string{"com.docker.compose.service": "mosquitto"},
							},
						},
					},
				},
			})
		case "SYNO.Docker.Container.list":
			write(map[string]any{
				"success": true,
				"data": map[string]any{
					"containers": []map[string]any{
						{"id": "c2", "name": "iptv", "image": "guovern/iptv-api", "ImageID": "sha256:imgid222", "status": "running"},
						{"id": "c3", "name": "customid", "image": "myalias:tagx", "ImageID": "sha256:imgid333", "status": "running"},
					},
				},
			})
		case "SYNO.Docker.Image.list":
			write(map[string]any{
				"success": true,
				"data": map[string]any{
					"images": []map[string]any{
						{"id": "sha256:imgid111", "repository": "library/eclipse-mosquitto", "tags": []string{"2"}, "digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
						{"id": "sha256:imgid222", "repository": "docker.io/guovern/iptv-api", "tags": []string{"latest"}, "digest": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
						{"id": "imgid333", "repository": "realrepo/app", "tags": []string{"1.0"}, "digest": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
					},
				},
			})
		default:
			write(map[string]any{"success": true, "data": map[string]any{}})
		}
	}))
	defer srv.Close()

	ad := New("h-nas", nil, DSMConfig{
		URL:      srv.URL,
		Username: "admin",
		Secret:   "pass",
	})
	defer ad.Close()

	apps, err := ad.ListApps(t.Context())
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	byName := make(map[string][]string)
	for _, a := range apps {
		for _, s := range a.Services {
			byName[s.Name] = s.RepoDigests
		}
	}
	if len(byName["mosquitto"]) == 0 || !strings.Contains(byName["mosquitto"][0], "sha256:aaaa") {
		t.Fatalf("expected eclipse-mosquitto:2 to match library/eclipse-mosquitto:2 digest, got %v", byName["mosquitto"])
	}
	if len(byName["iptv"]) == 0 || !strings.Contains(byName["iptv"][0], "sha256:bbbb") {
		t.Fatalf("expected guovern/iptv-api (implicit :latest) to match digest, got %v", byName["iptv"])
	}
	if len(byName["customid"]) == 0 || !strings.Contains(byName["customid"][0], "sha256:cccc") {
		t.Fatalf("expected container matched via ImageID fallback to get digest, got %v", byName["customid"])
	}
}

func TestDSMProjectComposeReadAndUpdateRollback(t *testing.T) {
	origYAML := "services:\n  web:\n    image: ghcr.io/org/web:1.26.0\n"
	curContent := origYAML
	failBuild := true
	pullStartCalled := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		apiName := r.FormValue("api")
		method := r.FormValue("method")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch apiName + "." + method {
		case "SYNO.API.Auth.login":
			write(map[string]any{"success": true, "data": map[string]any{"sid": "sid-1"}})
		case "SYNO.Docker.Project.get":
			write(map[string]any{"success": true, "data": map[string]any{"id": "p1", "name": "webstack", "path": "/volume1/docker/webstack", "content": curContent}})
		case "SYNO.Docker.Image.pull_start":
			pullStartCalled = true
			write(map[string]any{"success": false, "error": map[string]any{"code": 1202}})
		case "SYNO.Docker.Project.update":
			_ = json.Unmarshal([]byte(r.FormValue("content")), &curContent)
			write(map[string]any{"success": true, "data": map[string]any{}})
		case "SYNO.Docker.Project.build_stream", "SYNO.Docker.Project.build":
			if failBuild && strings.Contains(curContent, "1.27.3") {
				write(map[string]any{"success": false, "error": map[string]any{"code": 500}})
				return
			}
			write(map[string]any{"success": true, "data": map[string]any{}})
		default:
			write(map[string]any{"success": true, "data": map[string]any{}})
		}
	}))
	defer srv.Close()

	ad := New("h-nas", nil, DSMConfig{URL: srv.URL, Username: "admin", Secret: "pass"})
	defer ad.Close()

	proj := &platform.ProjectInfo{Name: "webstack", DSMProjectID: "p1"}
	gotYAML, err := ad.GetProjectCompose(t.Context(), proj)
	if err != nil || gotYAML != origYAML {
		t.Fatalf("GetProjectCompose got=%q err=%v", gotYAML, err)
	}

	newYAML := "services:\n  web:\n    image: ghcr.io/org/web:1.27.3\n"
	if err := ad.UpdateProjectCompose(t.Context(), "webstack", proj, newYAML, nil); err == nil {
		t.Fatal("expected build failure")
	}
	if curContent != origYAML {
		t.Fatalf("expected restoreProjectYAML to restore origYAML, got:\n%s", curContent)
	}
	if pullStartCalled {
		t.Fatal("UpdateProjectCompose on DSM Project must stream build_stream directly without failing on pull_start 1202")
	}
}

func TestDSMStandaloneConfigReadAndUpdateWithBackupRestore(t *testing.T) {
	curProfile := map[string]any{
		"name":  "myapp",
		"image": "ghcr.io/org/myapp:1.0.0",
		"env_variables": []any{
			map[string]any{"key": "TZ", "value": "Asia/Shanghai"},
		},
		"port_bindings": []any{
			map[string]any{"container_port": 8080, "host_port": 18080, "type": "tcp"},
		},
		"volume_bindings": []any{
			map[string]any{"host_volume_file": "/docker/myapp", "mount_point": "/data", "type": "rw"},
		},
	}
	curImgID := "sha256:old1"
	failCreateNew := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		apiName := r.FormValue("api")
		method := r.FormValue("method")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch apiName + "." + method {
		case "SYNO.API.Auth.login":
			write(map[string]any{"success": true, "data": map[string]any{"sid": "sid-1"}})
		case "SYNO.Docker.Container.list":
			write(map[string]any{"success": true, "data": map[string]any{
				"containers": []any{map[string]any{
					"id": "c-standalone", "name": "myapp", "Image": curProfile["image"], "ImageID": curImgID,
					"status": "running", "State": map[string]any{"Running": true, "Status": "running"},
				}},
			}})
		case "SYNO.Docker.Container.get":
			write(map[string]any{"success": true, "data": map[string]any{
				"profile": curProfile,
				"details": map[string]any{"State": map[string]any{"Running": true}},
			}})
		case "SYNO.Docker.Image.pull_start":
			write(map[string]any{"success": true, "data": map[string]any{"task_id": ""}})
		case "SYNO.Docker.Image.list":
			write(map[string]any{"success": true, "data": map[string]any{
				"images": []any{map[string]any{"id": "sha256:new2", "repository": "org/myapp", "tags": []string{"1.1.0"}}},
			}})
		case "SYNO.Docker.Container.stop", "SYNO.Docker.Container.delete":
			write(map[string]any{"success": true, "data": map[string]any{}})
		case "SYNO.Docker.Container.create":
			var p map[string]any
			_ = json.Unmarshal([]byte(r.FormValue("profile")), &p)
			if failCreateNew && p["image"] == "ghcr.io/org/myapp:1.1.0" {
				write(map[string]any{"success": false, "error": map[string]any{"code": 1300}})
				return
			}
			curProfile = p
			if p["image"] == "ghcr.io/org/myapp:1.1.0" {
				curImgID = "sha256:new2"
			} else {
				curImgID = "sha256:old1"
			}
			write(map[string]any{"success": true, "data": map[string]any{}})
		default:
			write(map[string]any{"success": true, "data": map[string]any{}})
		}
	}))
	defer srv.Close()

	ad := New("h-nas", nil, DSMConfig{URL: srv.URL, Username: "admin", Secret: "pass"})
	defer ad.Close()

	cfg, err := ad.GetStandaloneConfig(t.Context(), "myapp", "main")
	if err != nil {
		t.Fatalf("GetStandaloneConfig failed: %v", err)
	}
	if !cfg.Editable || len(cfg.Env) != 1 || cfg.Env[0].Key != "TZ" || len(cfg.Ports) != 1 || cfg.Ports[0].HostPort != "18080" || len(cfg.Volumes) != 1 {
		t.Fatalf("unexpected standalone config: %+v", cfg)
	}

	// 1. Test failure restores original profile
	failCreateNew = true
	_, err = ad.UpdateStandaloneConfig(t.Context(), platform.StandaloneUpdateRequest{
		AppName:        "myapp",
		ServiceName:    "main",
		OldImageRef:    "ghcr.io/org/myapp:1.0.0",
		TargetImageRef: "ghcr.io/org/myapp:1.1.0",
		Env:            []platform.EnvVar{{Key: "TZ", Value: "UTC"}},
		Ports:          []platform.PortBinding{{HostPort: "28080", ContainerPort: "8080", Protocol: "tcp"}},
	}, nil)
	if err == nil || curProfile["image"] != "ghcr.io/org/myapp:1.0.0" {
		t.Fatalf("expected failure and backup restore, got err=%v profile=%v", err, curProfile)
	}

	// 2. Test success updates image, env, and ports
	failCreateNew = false
	res, err := ad.UpdateStandaloneConfig(t.Context(), platform.StandaloneUpdateRequest{
		AppName:        "myapp",
		ServiceName:    "main",
		OldImageRef:    "ghcr.io/org/myapp:1.0.0",
		TargetImageRef: "ghcr.io/org/myapp:1.1.0",
		Env:            []platform.EnvVar{{Key: "TZ", Value: "UTC"}, {Key: "NEW_FLAG", Value: "1"}},
		Ports:          []platform.PortBinding{{HostPort: "28080", ContainerPort: "8080", Protocol: "tcp"}},
	}, nil)
	if err != nil || res.NewImageID != "sha256:new2" {
		t.Fatalf("UpdateStandaloneConfig failed: res=%+v err=%v", res, err)
	}
	cfgAfter, _ := ad.GetStandaloneConfig(t.Context(), "myapp", "main")
	if len(cfgAfter.Env) != 2 || cfgAfter.Ports[0].HostPort != "28080" {
		t.Fatalf("expected updated env and ports, got %+v", cfgAfter)
	}
}

func TestListRuntimeAppsViaDSMAPI_FastModeSkipsStatsAndProjectGet(t *testing.T) {
	var projGetHits, statsHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		apiName := r.FormValue("api")
		method := r.FormValue("method")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch apiName + "." + method {
		case "SYNO.API.Auth.login":
			write(map[string]any{"success": true, "data": map[string]any{"sid": "sid-rt"}})
		case "SYNO.Docker.Project.list":
			write(map[string]any{
				"success": true,
				"data": map[string]any{
					"p1": map[string]any{
						"id":           "p1",
						"name":         "immich",
						"path":         "/volume1/docker/immich",
						"status":       "RUNNING",
						"containerIds": []string{"cid1111111111111", "cid2222222222222"},
					},
				},
			})
		case "SYNO.Docker.Project.get":
			projGetHits.Add(1)
			write(map[string]any{"success": true, "data": map[string]any{}})
		case "SYNO.Docker.Container.list":
			write(map[string]any{
				"success": true,
				"data": map[string]any{
					"containers": []map[string]any{
						{
							"id":     "cid1111111111111",
							"name":   "immich-server",
							"Image":  "ghcr.io/immich-app/immich-server:release",
							"status": "running",
							"Labels": map[string]string{"com.docker.compose.project": "immich", "com.docker.compose.service": "immich-server"},
							"State":  map[string]any{"Running": true, "Status": "running"},
						},
						{
							"id":     "cid2222222222222",
							"name":   "immich-redis",
							"Image":  "redis:7-alpine",
							"status": "running",
							"Labels": map[string]string{"com.docker.compose.project": "immich", "com.docker.compose.service": "redis"},
							"State":  map[string]any{"Running": true, "Status": "running"},
						},
					},
				},
			})
		case "SYNO.Docker.Container.stats":
			hit := statsHits.Add(1)
			write(map[string]any{
				"success": true,
				"data": map[string]any{
					"immich-server": map[string]any{
						"cpu_stats": map[string]any{
							"cpu_usage":        map[string]any{"total_usage": uint64(hit) * 40_000_000},
							"system_cpu_usage": uint64(hit) * 1_000_000_000,
							"online_cpus":      2,
						},
						"memory_stats": map[string]any{"usage": uint64(128 * 1024 * 1024), "limit": uint64(2048 * 1024 * 1024)},
					},
				},
			})
		default:
			write(map[string]any{"success": true, "data": map[string]any{}})
		}
	}))
	defer srv.Close()

	ad := New("h-nas", nil, DSMConfig{URL: srv.URL, Username: "admin", Secret: "pass"})
	defer ad.Close()

	// Fast mode (withStats=false): 0 stats hits, 0 Project.get hits!
	fast, err := ad.ListRuntimeAppsWithOptions(t.Context(), false)
	if err != nil || len(fast) != 1 || len(fast[0].Containers) != 2 {
		t.Fatalf("unexpected fast result: len=%d err=%v", len(fast), err)
	}
	if fast[0].StatsReady || statsHits.Load() != 0 || projGetHits.Load() != 0 {
		t.Fatalf("expected 0 statsHits & 0 projGetHits, got stats=%d projGet=%d", statsHits.Load(), projGetHits.Load())
	}

	// Full stats mode: populates stats without N+1 Project.get, and warm call uses only 1 stats RPC
	full1, err := ad.ListRuntimeAppsWithOptions(t.Context(), true)
	if err != nil || !full1[0].StatsReady || full1[0].CPUPercent <= 0 || projGetHits.Load() != 0 {
		t.Fatalf("unexpected full1: %+v projGet=%d err=%v", full1, projGetHits.Load(), err)
	}
	hitsAfterCold := statsHits.Load()
	full2, err := ad.ListRuntimeAppsWithOptions(t.Context(), true)
	if err != nil || statsHits.Load() != hitsAfterCold+1 || full2[0].CPUPercent <= 0 {
		t.Fatalf("expected warm call to do only 1 stats RPC, before=%d after=%d cpu=%v err=%v", hitsAfterCold, statsHits.Load(), full2[0].CPUPercent, err)
	}
}

func TestDSMProjectCompose_RejectsUnconfirmedServiceRemovalAndCleansWhenConfirmed(t *testing.T) {
	origYAML := "services:\n  web:\n    image: nginx:1.26.0\n  db:\n    image: postgres:16-alpine\n"
	curContent := origYAML
	var updateCalled, buildCalled bool
	var stoppedContainers, deletedContainers []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		apiName := r.FormValue("api")
		method := r.FormValue("method")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch apiName + "." + method {
		case "SYNO.API.Auth.login":
			write(map[string]any{"success": true, "data": map[string]any{"sid": "sid-1"}})
		case "SYNO.Docker.Project.get":
			write(map[string]any{
				"success": true,
				"data": map[string]any{
					"id":      "p1",
					"name":    "webstack",
					"path":    "/volume1/docker/webstack",
					"content": curContent,
					"containers": []any{
						map[string]any{
							"Id":     "c-db",
							"Name":   "/webstack-db-1",
							"Config": map[string]any{"Image": "postgres:16-alpine", "Labels": map[string]string{"com.docker.compose.service": "db"}},
							"State":  map[string]any{"Running": true, "Status": "running"},
						},
					},
				},
			})
		case "SYNO.Docker.Container.list":
			write(map[string]any{"success": true, "data": map[string]any{"containers": []any{}}})
		case "SYNO.Docker.Project.update":
			updateCalled = true
			_ = json.Unmarshal([]byte(r.FormValue("content")), &curContent)
			write(map[string]any{"success": true, "data": map[string]any{}})
		case "SYNO.Docker.Project.build_stream", "SYNO.Docker.Project.build":
			buildCalled = true
			write(map[string]any{"success": true, "data": map[string]any{}})
		case "SYNO.Docker.Container.stop":
			var n string
			_ = json.Unmarshal([]byte(r.FormValue("name")), &n)
			stoppedContainers = append(stoppedContainers, n)
			write(map[string]any{"success": true, "data": map[string]any{}})
		case "SYNO.Docker.Container.delete":
			var n string
			_ = json.Unmarshal([]byte(r.FormValue("name")), &n)
			deletedContainers = append(deletedContainers, n)
			write(map[string]any{"success": true, "data": map[string]any{}})
		default:
			write(map[string]any{"success": true, "data": map[string]any{}})
		}
	}))
	defer srv.Close()

	ad := New("h-nas", nil, DSMConfig{URL: srv.URL, Username: "admin", Secret: "pass"})
	defer ad.Close()

	proj := &platform.ProjectInfo{Name: "webstack", DSMProjectID: "p1"}
	omittedDB := "services:\n  web:\n    image: nginx:1.27.3\n"

	// 1. 未确认移除时必须拦截，不得发起 SYNO.Docker.Project update / build_stream
	err := ad.UpdateProjectCompose(t.Context(), "webstack", proj, omittedDB, nil)
	var csrErr *platform.ComposeServicesRemovedError
	if !errors.As(err, &csrErr) || len(csrErr.RemovedServices) != 1 || csrErr.RemovedServices[0] != "db" {
		t.Fatalf("expected ComposeServicesRemovedError for db, got %v", err)
	}
	if updateCalled || buildCalled {
		t.Fatalf("DSM WebAPI update/build_stream must NOT be called before confirmation (update=%v build=%v)", updateCalled, buildCalled)
	}

	// 2. 确认后允许更新，并对残留旧服务容器执行确定性停止与删除
	if err := ad.UpdateProjectComposeWithOptions(t.Context(), "webstack", proj, omittedDB, platform.ComposeUpdateOptions{
		ConfirmRemoveServices: true,
	}, nil); err != nil {
		t.Fatalf("confirmed DSM compose update failed: %v", err)
	}
	if !updateCalled || !buildCalled {
		t.Fatalf("expected update & build_stream after confirmation")
	}
	if len(stoppedContainers) != 1 || stoppedContainers[0] != "webstack-db-1" || len(deletedContainers) != 1 || deletedContainers[0] != "webstack-db-1" {
		t.Fatalf("expected leftover db container to be stopped & deleted, got stopped=%v deleted=%v", stoppedContainers, deletedContainers)
	}
}

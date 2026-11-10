package synology

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	origYAML := "services:\n  web:\n    image: nginx:1.26.0\n"
	curContent := origYAML
	failBuild := true
	var pulledRepos []string

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
			var repo string
			_ = json.Unmarshal([]byte(r.FormValue("repository")), &repo)
			pulledRepos = append(pulledRepos, repo)
			write(map[string]any{"success": true, "data": map[string]any{"task_id": ""}})
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

	newYAML := "services:\n  web:\n    image: nginx:1.27.3\n"
	if err := ad.UpdateProjectCompose(t.Context(), "webstack", proj, newYAML, nil); err == nil {
		t.Fatal("expected build failure")
	}
	if curContent != origYAML {
		t.Fatalf("expected restoreProjectYAML to restore origYAML, got:\n%s", curContent)
	}
	if len(pulledRepos) == 0 || pulledRepos[0] != "nginx" {
		t.Fatalf("expected PullImage before build, got %v", pulledRepos)
	}
}

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

type fakeExecTransport struct {
	doHandler  func(path string) (*http.Response, error)
	reqHandler func(method, path string, body []byte) (*http.Response, error)
	files      map[string][]byte
}

func (f *fakeExecTransport) Mode() transport.Mode { return transport.ModeLocal }
func (f *fakeExecTransport) Close() error         { return nil }
func (f *fakeExecTransport) DoDocker(_ context.Context, path string) (*http.Response, error) {
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

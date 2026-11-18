package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"strings"
	"testing"

	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/transport"
)

type fakeFSTransport struct {
	*fakeExecTransport
	accessMap   map[string]transport.FileAccessState
	defaultPerm transport.FileAccessState
	dirsMap     map[string][]string
	homeDir     string
}

func (f *fakeFSTransport) ProbeFileAccess(_ context.Context, filePath string) (transport.FileAccessState, error) {
	if st, ok := f.accessMap[filePath]; ok {
		return st, nil
	}
	if _, ok := f.files[filePath]; ok {
		return transport.FileAccessState{Exists: true, Readable: true, Writable: true}, nil
	}
	return f.defaultPerm, nil
}

func (f *fakeFSTransport) ListDirectories(_ context.Context, dirPath string) (*transport.DirListing, error) {
	clean := normalizeComposePosixPath(dirPath)
	return &transport.DirListing{
		Path:     clean,
		Dirs:     f.dirsMap[clean],
		Writable: true,
		HomeDir:  f.homeDir,
	}, nil
}

func (f *fakeFSTransport) CreateDirectory(_ context.Context, dirPath string) error {
	clean := normalizeComposePosixPath(dirPath)
	parent := path.Dir(clean)
	base := path.Base(clean)
	f.dirsMap[parent] = append(f.dirsMap[parent], base)
	return nil
}

func makeJSONHTTPResp(v any) (*http.Response, error) {
	b, _ := json.Marshal(v)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(b)),
	}, nil
}

func TestInspectProjectComposeEdit_MissingPathReconstructsAndCheckCollisions(t *testing.T) {
	containers := []map[string]any{
		{
			"Id":    "3361eafab2c50000",
			"Names": []string{"/sgcc_electricity"},
			"Image": "registry.cn-hangzhou.aliyuncs.com/arcw/sgcc_electricity:latest",
			"State": "running",
			"Labels": map[string]string{
				"com.docker.compose.project":             "sgcc_electricity",
				"com.docker.compose.service":             "app",
				"com.docker.compose.project.working_dir": "/data/compose/19",
			},
		},
		{
			"Id":    "934c4ee3eec80000",
			"Names": []string{"/qinglong"},
			"Image": "whyour/qinglong:2.24.0",
			"State": "running",
			"Labels": map[string]string{
				"com.docker.compose.project":             "ql_proj",
				"com.docker.compose.service":             "ql",
				"com.docker.compose.project.working_dir": "/opt/docker/qinglong",
			},
		},
		{
			"Id":    "deadbeef12340000",
			"Names": []string{"/old-tool"},
			"Image": "demo/old-tool:1.0",
			"State": "exited",
			"Labels": map[string]string{
				"com.docker.compose.project":             "old_proj",
				"com.docker.compose.service":             "web",
				"com.docker.compose.project.working_dir": "/opt/docker/old-tool",
			},
		},
	}

	inspectMap := map[string]map[string]any{
		"3361eafab2c50000": {
			"Id":    "3361eafab2c50000",
			"Name":  "/sgcc_electricity",
			"Image": "sha256:img_sgcc",
			"Config": map[string]any{
				"Image": "registry.cn-hangzhou.aliyuncs.com/arcw/sgcc_electricity:latest",
				"Env":   []string{"PATH=/usr/local/bin:/usr/bin", "TZ=Asia/Shanghai"},
			},
			"HostConfig": map[string]any{
				"NetworkMode":   "sgcc_electricity_default",
				"RestartPolicy": map[string]any{"Name": "unless-stopped"},
			},
			"Mounts": []map[string]any{
				{
					"Type":        "bind",
					"Source":      "/sgcc_electricity_new/data",
					"Destination": "/app/data",
					"RW":          true,
				},
			},
		},
	}

	baseTr := &fakeExecTransport{
		files: map[string][]byte{
			"/opt/docker/qinglong/compose.yaml":  []byte("services:\n  ql:\n    image: whyour/qinglong:2.24.0\n"),
			"/opt/docker/old-tool/compose.yaml":  []byte("services:\n  web:\n    image: demo/old-tool:1.0\n"),
			"/sgcc_electricity_new/compose.yaml": []byte("services:\n  app:\n    image: registry.cn-hangzhou.aliyuncs.com/arcw/sgcc_electricity:latest\n"),
		},
		doHandler: func(p string) (*http.Response, error) {
			switch {
			case p == "/containers/json?all=1":
				return makeJSONHTTPResp(containers)
			case strings.HasPrefix(p, "/containers/3361eafab2c50000/json"):
				return makeJSONHTTPResp(inspectMap["3361eafab2c50000"])
			case strings.HasPrefix(p, "/images/"):
				return makeJSONHTTPResp(map[string]any{
					"Config": map[string]any{"Env": []string{"PATH=/usr/local/bin:/usr/bin"}},
				})
			default:
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found"))}, nil
			}
		},
	}

	fsTr := &fakeFSTransport{
		fakeExecTransport: baseTr,
		accessMap: map[string]transport.FileAccessState{
			"/data/compose/19/compose.yaml":     {Exists: false, Readable: false, Writable: true},
			"/opt/docker/empty/compose.yaml":    {Exists: false, Readable: false, Writable: true},
			"/rom/compose.yaml":                 {Exists: false, Readable: false, Writable: false},
			"/opt/docker/qinglong/compose.yaml": {Exists: true, Readable: true, Writable: true},
			"/opt/docker/old-tool/compose.yaml": {Exists: true, Readable: true, Writable: true},
		},
		defaultPerm: transport.FileAccessState{Exists: false, Readable: false, Writable: true},
		dirsMap: map[string][]string{
			"/": {"data", "opt", "sgcc_electricity_new"},
		},
		homeDir: "/root",
	}

	ad := New("h1", fsTr)
	proj := &platform.ProjectInfo{
		Name:       "sgcc_electricity",
		WorkingDir: "/data/compose/19",
	}

	insp, err := ad.InspectProjectComposeEdit(context.Background(), "sgcc_electricity", proj)
	if err != nil {
		t.Fatalf("InspectProjectComposeEdit error: %v", err)
	}
	if !insp.PathMissing || !insp.OrigWritable || insp.ComposePath != "/data/compose/19/compose.yaml" {
		t.Fatalf("unexpected inspection flags: %+v", insp)
	}
	if !strings.Contains(insp.Content, "container_name: sgcc_electricity") ||
		!strings.Contains(insp.Content, "TZ=Asia/Shanghai") ||
		strings.Contains(insp.Content, "PATH=/usr/local/bin") ||
		!strings.Contains(insp.Content, "/sgcc_electricity_new/data:/app/data") {
		t.Fatalf("unexpected reconstructed YAML:\n%s", insp.Content)
	}

	// 1) 运行中其他容器目录：必须拦截 other_running
	chkRun, err := ad.CheckComposeSaveDir(context.Background(), "sgcc_electricity", proj, "/opt/docker/qinglong", "")
	if err != nil || chkRun.ExistKind != "other_running" || !strings.Contains(chkRun.Owner, "qinglong") {
		t.Fatalf("expected other_running for qinglong, got %+v err=%v", chkRun, err)
	}

	// 2) 已停止其他容器目录：返回 other_stopped
	chkStop, err := ad.CheckComposeSaveDir(context.Background(), "sgcc_electricity", proj, "/opt/docker/old-tool", "")
	if err != nil || chkStop.ExistKind != "other_stopped" || !strings.Contains(chkStop.Owner, "old-tool") {
		t.Fatalf("expected other_stopped for old-tool, got %+v err=%v", chkStop, err)
	}

	// 3) 同项目已有 compose.yaml：返回 same
	chkSame, err := ad.CheckComposeSaveDir(context.Background(), "sgcc_electricity", proj, "/sgcc_electricity_new", "")
	if err != nil || chkSame.ExistKind != "same" {
		t.Fatalf("expected same for /sgcc_electricity_new, got %+v err=%v", chkSame, err)
	}

	// 4) 只读目录：返回 Writable=false
	chkRO, err := ad.CheckComposeSaveDir(context.Background(), "sgcc_electricity", proj, "/rom", "")
	if err != nil || chkRO.Writable {
		t.Fatalf("expected Writable=false for /rom, got %+v err=%v", chkRO, err)
	}

	// 5) 服务端执行更新时同样拦截覆盖运行中的其他容器配置目录
	err = ad.UpdateProjectComposeWithOptions(context.Background(), "sgcc_electricity", proj, insp.Content, platform.ComposeUpdateOptions{
		TargetComposePath: "/opt/docker/qinglong/compose.yaml",
	}, nil)
	if err == nil {
		t.Fatal("expected UpdateProjectComposeWithOptions to reject overwriting running container compose dir")
	}
}

func TestRelocateComposeRelativePaths(t *testing.T) {
	orig := `services:
  web:
    image: nginx:alpine
    env_file:
      - ./.env.prod
    volumes:
      - ./data:/usr/share/nginx/html:ro
      - /etc/localtime:/etc/localtime:ro
`
	got := RelocateComposeRelativePaths(orig, "/old/stack", "/new/folder")
	if !strings.Contains(got, "/old/stack/data:/usr/share/nginx/html:ro") {
		t.Fatalf("expected ./data converted to /old/stack/data, got:\n%s", got)
	}
	if !strings.Contains(got, "/old/stack/.env.prod") {
		t.Fatalf("expected ./.env.prod converted to /old/stack/.env.prod, got:\n%s", got)
	}
}

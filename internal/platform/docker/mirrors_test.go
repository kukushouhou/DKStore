package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/transport"
)

func TestUpdateDaemonJSONMirrorsPreservesOtherFields(t *testing.T) {
	orig := []byte(`{"data-root":"/data/docker","registry-mirrors":["https://docker.mirrors.ustc.edu.cn/"],"storage-driver":"overlay2"}`)
	urls, ok := ExtractMirrorsFromDaemonJSON(orig)
	if !ok || len(urls) != 1 || urls[0] != "https://docker.mirrors.ustc.edu.cn" {
		t.Fatalf("unexpected extracted mirrors: ok=%v urls=%v", ok, urls)
	}

	updated, _, err := UpdateDaemonJSONMirrors(orig, []string{"docker.1ms.run/", "https://docker.m.daocloud.io"})
	if err != nil {
		t.Fatalf("UpdateDaemonJSONMirrors failed: %v", err)
	}
	s := string(updated)
	if !strings.Contains(s, `"/data/docker"`) || !strings.Contains(s, `"overlay2"`) {
		t.Fatalf("expected existing fields preserved, got: %s", s)
	}
	nextURLs, ok2 := ExtractMirrorsFromDaemonJSON(updated)
	if !ok2 || len(nextURLs) != 2 || nextURLs[0] != "https://docker.1ms.run" || nextURLs[1] != "https://docker.m.daocloud.io" {
		t.Fatalf("unexpected updated mirrors: %v", nextURLs)
	}

	cleared, _, err := UpdateDaemonJSONMirrors(updated, nil)
	if err != nil {
		t.Fatalf("clear mirrors failed: %v", err)
	}
	if strings.Contains(string(cleared), "registry-mirrors") {
		t.Fatalf("expected registry-mirrors key deleted when empty, got: %s", string(cleared))
	}
}

func TestNormalizeMirrorPairsAndBuildMirrorRef(t *testing.T) {
	pairs := NormalizeMirrorPairs([]platform.MirrorPair{
		{Source: "https://index.docker.io/", URLs: []string{"docker.1ms.run/", "https://docker.1ms.run"}},
		{Source: "https://ghcr.io/", URLs: []string{"ghcr.nju.edu.cn"}},
	})
	if len(pairs) != 2 || pairs[0].Source != "docker.io" || len(pairs[0].URLs) != 1 || pairs[1].Source != "ghcr.io" {
		t.Fatalf("unexpected normalized pairs: %+v", pairs)
	}

	reg, repo, tag := splitImageRegistryAndRepoPath("ghcr.io/owner/app:v1.2.3")
	if reg != "ghcr.io" || repo != "owner/app" || tag != "v1.2.3" {
		t.Fatalf("unexpected split: %s / %s / %s", reg, repo, tag)
	}
	ref := buildMirrorPullRef("https://ghcr.nju.edu.cn/", repo, tag)
	if ref != "ghcr.nju.edu.cn/owner/app:v1.2.3" {
		t.Fatalf("unexpected mirror pull ref: %s", ref)
	}

	// Docker 29+ containerd snapshotter 镜像名常带 tag@sha256:... 后缀，应提取出纯标签而不将 sha256 混入冒号标签
	reg2, repo2, tag2 := splitImageRegistryAndRepoPath("mysql:8.4.11@sha256:6ea90827b1100f8f2ae306a539f86d2c264a26ed435a2a9f75551dd5c3aeb242")
	if reg2 != "docker.io" || repo2 != "library/mysql" || tag2 != "8.4.11" {
		t.Fatalf("unexpected tag@digest split: %s / %s / %s", reg2, repo2, tag2)
	}
	// 纯摘要无标签引用应保留 sha256:... 并在代拉时使用 @ 连接
	reg3, repo3, tag3 := splitImageRegistryAndRepoPath("ghcr.io/owner/app@sha256:abcd1234")
	if reg3 != "ghcr.io" || repo3 != "owner/app" || tag3 != "sha256:abcd1234" {
		t.Fatalf("unexpected untagged digest split: %s / %s / %s", reg3, repo3, tag3)
	}
	if dRef := buildMirrorPullRef("https://ghcr.nju.edu.cn", repo3, tag3); dRef != "ghcr.nju.edu.cn/owner/app@sha256:abcd1234" {
		t.Fatalf("unexpected digest mirror pull ref: %s", dRef)
	}
}

func TestOpenWrtDockerdUCIMirrorsReadWrite(t *testing.T) {
	origUCI := []byte("# OpenWrt dockerd config\nconfig globals 'globals'\n\toption data_root '/opt/docker/'\n\toption log_level 'warn'\n\tlist registry_mirrors 'https://old-mirror.example.com/'\n\toption iptables '1'\n\nconfig firewall 'firewall'\n\toption device 'docker0'\n")
	extracted := ExtractMirrorsFromOpenWrtUCI(origUCI)
	if len(extracted) != 1 || extracted[0] != "https://old-mirror.example.com" {
		t.Fatalf("unexpected extracted UCI mirrors: %v", extracted)
	}

	updated, changed := UpdateOpenWrtDockerdUCI(origUCI, []string{"docker.1ms.run", "https://m.daocloud.io/"})
	if !changed {
		t.Fatalf("expected UCI changed=true")
	}
	uStr := string(updated)
	if !strings.Contains(uStr, "option data_root '/opt/docker/'") || !strings.Contains(uStr, "config firewall 'firewall'") {
		t.Fatalf("expected existing UCI sections preserved, got:\n%s", uStr)
	}
	after := ExtractMirrorsFromOpenWrtUCI(updated)
	if len(after) != 2 || after[0] != "https://docker.1ms.run" || after[1] != "https://m.daocloud.io" {
		t.Fatalf("unexpected updated UCI mirrors: %v", after)
	}

	cleared, changed2 := UpdateOpenWrtDockerdUCI(updated, nil)
	if !changed2 || len(ExtractMirrorsFromOpenWrtUCI(cleared)) != 0 {
		t.Fatalf("expected UCI mirrors cleared cleanly, got:\n%s", string(cleared))
	}
	if !strings.Contains(string(cleared), "option iptables '1'") {
		t.Fatalf("expected other options intact after clearing mirrors")
	}
}

func TestRunMirrorProbeBatchClosedLoop(t *testing.T) {
	// 1. 全量可用加速源：支持 Bearer access_token + Manifest + 1B Blob 读取（且 old/deleted:1.0 返回 404 测试顺延）
	var fullSrv *httptest.Server
	fullSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/":
			w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
			w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="mirror"`, fullSrv.URL))
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"demo-jwt"}`))
		case r.URL.Path == "/v2/louislam/uptime-kuma/manifests/2.5.11":
			if r.Header.Get("Authorization") != "Bearer demo-jwt" {
				w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="mirror",scope="repository:louislam/uptime-kuma:pull"`, fullSrv.URL))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			_, _ = w.Write([]byte(`{"schemaVersion":2,"layers":[{"digest":"sha256:abc123"}]}`))
		case r.URL.Path == "/v2/louislam/uptime-kuma/blobs/sha256:abc123":
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("X"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer fullSrv.Close()

	// 2. 白名单限制源：/v2/ 返回 200 假活，拉具体镜像时返回 403
	whitelistSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer whitelistSrv.Close()

	// 3. 缺库源：/v2/ 在线，但未同步 uptime-kuma 返回 404（与 fullSrv 同批交叉复核应归类为 restricted）
	unsyncedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer unsyncedSrv.Close()

	// 4. 关停源：返回 200 text/html 停服公告页
	deadHTMLSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>Mirror Closed</html>"))
	}))
	defer deadHTMLSrv.Close()

	items := RunMirrorProbeBatch(context.Background(), fullSrv.Client(), platform.MirrorProbeRequest{
		Source:         "docker.io",
		URLs:           []string{fullSrv.URL, whitelistSrv.URL, unsyncedSrv.URL, deadHTMLSrv.URL},
		HasSpecificApp: true,
		Candidates: []platform.MirrorProbeCandidate{
			{RepoPath: "old/deleted", Tag: "1.0"},
			{RepoPath: "louislam/uptime-kuma", Tag: "2.5.11"},
		},
	})
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}
	if !items[0].OK || items[0].Status != "ok" || items[0].TestedImage != "louislam/uptime-kuma:2.5.11" {
		t.Fatalf("expected fullSrv ok after 404 candidate fallback, got %+v", items[0])
	}
	if items[1].OK || items[1].Status != "restricted" {
		t.Fatalf("expected whitelistSrv restricted, got %+v", items[1])
	}
	if items[2].OK || items[2].Status != "not_found" {
		t.Fatalf("expected unsyncedSrv not_found, got %+v", items[2])
	}
	if items[3].OK || items[3].Status != "dead" {
		t.Fatalf("expected deadHTMLSrv dead, got %+v", items[3])
	}
}

type stubMirrorReadTransport struct {
	readFn func(path string) ([]byte, error)
	doFn   func(path string) (*http.Response, error)
}

func (s *stubMirrorReadTransport) Mode() transport.Mode { return transport.ModeLocal }
func (s *stubMirrorReadTransport) Close() error         { return nil }
func (s *stubMirrorReadTransport) ReadFile(_ context.Context, path string) ([]byte, error) {
	return s.readFn(path)
}
func (s *stubMirrorReadTransport) DoDocker(_ context.Context, path string) (*http.Response, error) {
	return s.doFn(path)
}

func TestGetDockerHubMirrorsPropagatesReadFailure(t *testing.T) {
	ctx := context.Background()

	// 1. 权限不足或 SSH 传输断开时绝不返回看似正常的空列表
	adPermErr := New("h1", &stubMirrorReadTransport{
		readFn: func(string) ([]byte, error) {
			return nil, errors.New("permission denied")
		},
		doFn: func(string) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"RegistryConfig":{"Mirrors":[]}}`)),
			}, nil
		},
	})
	if urls, err := adPermErr.GetDockerHubMirrors(ctx); err == nil {
		t.Fatalf("expected error on ReadFile permission denied, got nil err and urls=%v", urls)
	}

	// 2. daemon.json 不存在且 /info 请求也失败（Docker 离线/断连）时必须报错
	adOffline := New("h2", &stubMirrorReadTransport{
		readFn: func(string) ([]byte, error) {
			return nil, os.ErrNotExist
		},
		doFn: func(string) (*http.Response, error) {
			return nil, errors.New("dial unix /var/run/docker.sock: connect: connection refused")
		},
	})
	if urls, err := adOffline.GetDockerHubMirrors(ctx); err == nil {
		t.Fatalf("expected error when both daemon.json missing and /info fails, got nil err and urls=%v", urls)
	}

	// 3. daemon.json 语法损坏时必须报错
	adBrokenJSON := New("h3", &stubMirrorReadTransport{
		readFn: func(string) ([]byte, error) {
			return []byte(`{"registry-mirrors": [broken`), nil
		},
		doFn: func(string) (*http.Response, error) {
			return nil, errors.New("unused")
		},
	})
	if urls, err := adBrokenJSON.GetDockerHubMirrors(ctx); err == nil {
		t.Fatalf("expected error on malformed daemon.json, got nil err and urls=%v", urls)
	}

	// 4. daemon.json 不存在但 Docker /info 正常返回无加速源时，确认为真正未配置（返回空列表、无错误）
	adUnconfigured := New("h4", &stubMirrorReadTransport{
		readFn: func(string) ([]byte, error) {
			return nil, os.ErrNotExist
		},
		doFn: func(string) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"RegistryConfig":{"Mirrors":null}}`)),
			}, nil
		},
	})
	urls, err := adUnconfigured.GetDockerHubMirrors(ctx)
	if err != nil || len(urls) != 0 {
		t.Fatalf("expected clean empty list when genuinely unconfigured, got urls=%v err=%v", urls, err)
	}
}

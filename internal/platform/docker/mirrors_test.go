package docker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kukushouhou/DKStore/internal/platform"
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

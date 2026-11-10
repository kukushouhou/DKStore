package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kukushouhou/DKStore/internal/secret"
	"github.com/kukushouhou/DKStore/internal/store"
	"github.com/kukushouhou/DKStore/internal/version"
)

func TestCheckManifestAndHubTagsWithBearer(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _ := secret.LoadOrCreate(dir)

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"test-jwt"}`))
		case "/v2/library/nginx/manifests/latest":
			if r.Header.Get("Authorization") != "Bearer test-jwt" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="registry.docker.io",scope="repository:library/nginx:pull"`, srvURL))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			w.Header().Set("Docker-Content-Digest", "sha256:indexdigest111")
			_, _ = w.Write([]byte(`{
				"manifests":[
					{"digest":"sha256:amd64digest222","platform":{"os":"linux","architecture":"amd64"}},
					{"digest":"sha256:arm64digest333","platform":{"os":"linux","architecture":"arm64"}}
				]
			}`))
		case "/v2/repositories/library/nginx/tags":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"next": "",
				"results": [
					{"name":"latest","digest":"sha256:indexdigest111","images":[{"os":"linux","architecture":"amd64","digest":"sha256:amd64digest222"}]},
					{"name":"1.27.3","digest":"sha256:indexdigest111","images":[{"os":"linux","architecture":"amd64","digest":"sha256:amd64digest222"}]},
					{"name":"1.27","digest":"sha256:indexdigest111","images":[{"os":"linux","architecture":"amd64","digest":"sha256:amd64digest222"}]}
				]
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	cli := New(st, box)
	cli.BaseURLOverride = map[string]string{
		"docker.io":      srv.URL,
		"hub.docker.com": srv.URL,
	}

	ref, _ := version.ParseImageRef("nginx:latest")
	mf, err := cli.CheckManifest(ctx, ref, "linux/amd64", false)
	if err != nil {
		t.Fatalf("CheckManifest: %v", err)
	}
	if mf.RepoDigest != "sha256:indexdigest111" || mf.PlatformDigest != "sha256:amd64digest222" {
		t.Fatalf("unexpected manifest result: %+v", mf)
	}

	tags, err := cli.FetchRecentTags(ctx, ref, 3, false)
	if err != nil {
		t.Fatalf("FetchRecentTags: %v", err)
	}
	matched := cli.LookupTagsByDigest(ctx, ref, mf.RepoDigest, mf.PlatformDigest, "linux/amd64", tags)
	best := version.SelectBestDisplayTag("latest", matched)
	if best != "1.27.3" {
		t.Fatalf("best display tag = %q, want 1.27.3 (matched=%v)", best, matched)
	}
}

func TestZeroQuotaHubManifestAndSearchTagsAndOCILookup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _ := secret.LoadOrCreate(dir)

	regGetCalls := 0
	ociHeadCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/repositories/library/eclipse-mosquitto/tags/2":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"digest": "sha256:hubindex999",
				"images": [{"os":"linux","architecture":"amd64","digest":"sha256:hubamd64888"}]
			}`))
		case r.URL.Path == "/v2/library/eclipse-mosquitto/manifests/2":
			regGetCalls++
			w.WriteHeader(http.StatusInternalServerError)
		case r.URL.Path == "/v2/repositories/library/eclipse-mosquitto/tags":
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("name") == "2.0.1" {
				_, _ = w.Write([]byte(`{
					"next": "",
					"results": [
						{"name":"2.0.18","digest":"sha256:old18","last_updated":"2026-10-01T12:00:00Z","images":[]}
					]
				}`))
				return
			}
			_, _ = w.Write([]byte(`{
				"next": "",
				"results": [
					{"name":"latest","digest":"sha256:hubindex999","last_updated":"2026-11-10T08:00:00Z","images":[]},
					{"name":"2.0.20","digest":"sha256:hubindex999","last_updated":"2026-11-10T08:00:00Z","images":[]}
				]
			}`))
		case r.URL.Path == "/v2/org/custom/tags/list":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tags":["latest","20261101-1","20261110-3"]}`))
		case r.URL.Path == "/v2/org/custom/manifests/20261110-3" && r.Method == http.MethodHead:
			ociHeadCalls++
			w.Header().Set("Docker-Content-Digest", "sha256:ocidate333")
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v2/org/custom/manifests/20261101-1" && r.Method == http.MethodHead:
			ociHeadCalls++
			w.Header().Set("Docker-Content-Digest", "sha256:ocidate111")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cli := New(st, box)
	cli.BaseURLOverride = map[string]string{
		"docker.io":      srv.URL,
		"hub.docker.com": srv.URL,
		"ghcr.io":        srv.URL,
	}

	// 1. Docker Hub 0-quota check via Hub Web API (Registry v2 GET must NOT be called)
	refHub, _ := version.ParseImageRef("eclipse-mosquitto:2")
	mf, err := cli.CheckManifestEx(ctx, refHub, ManifestCheckOptions{
		HostPlatform: "linux/amd64",
		BypassCache:  true,
	})
	if err != nil {
		t.Fatalf("CheckManifestEx: %v", err)
	}
	if regGetCalls != 0 || mf.RepoDigest != "sha256:hubindex999" || mf.PlatformDigest != "sha256:hubamd64888" {
		t.Fatalf("expected 0 registry GET calls and valid digests, got calls=%d mf=%+v", regGetCalls, mf)
	}

	// 2. SearchTags local vs remote
	localItems, highest, err := cli.SearchTags(ctx, refHub, "", false)
	if err != nil || highest != "2.0.20" || len(localItems) != 2 || localItems[0].UpdatedAt == 0 {
		t.Fatalf("SearchTags local unexpected: err=%v highest=%q items=%+v", err, highest, localItems)
	}
	remItems, highest2, err := cli.SearchTags(ctx, refHub, "2.0.1", true)
	if err != nil || highest2 != "2.0.20" || len(remItems) != 1 || remItems[0].Name != "2.0.18" {
		t.Fatalf("SearchTags remote unexpected: err=%v highest=%q items=%+v", err, highest2, remItems)
	}

	// 3. Non-DockerHub OCI alias tag lookup via HEAD (date tag 20261110-3)
	refOCI, _ := version.ParseImageRef("ghcr.io/org/custom:latest")
	ociRecent, err := cli.FetchRecentTags(ctx, refOCI, 1, true)
	if err != nil {
		t.Fatalf("FetchRecentTags OCI: %v", err)
	}
	matchedOCI := cli.LookupTagsByDigest(ctx, refOCI, "sha256:ocidate333", "", "linux/amd64", ociRecent)
	if len(matchedOCI) != 1 || matchedOCI[0] != "20261110-3" || ociHeadCalls == 0 {
		t.Fatalf("expected OCI date alias 20261110-3 via HEAD, got matched=%v headCalls=%d", matchedOCI, ociHeadCalls)
	}
}

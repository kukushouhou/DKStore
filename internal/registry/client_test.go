package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestSearchTagsVersionOrderingAndVariantFiltering(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _ := secret.LoadOrCreate(dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v2/repositories/library/eclipse-mosquitto/tags"):
			_, _ = w.Write([]byte(`{
				"next": "",
				"results": [
					{"name":"1.6.15-openssl","digest":"sha256:oldsec","last_updated":"2026-11-07T16:52:00Z","images":[]},
					{"name":"1.6-openssl","digest":"sha256:oldsec","last_updated":"2026-11-07T16:52:00Z","images":[]},
					{"name":"latest","digest":"sha256:mosq212","last_updated":"2026-11-07T01:52:00Z","images":[]},
					{"name":"alpine","digest":"sha256:mosq212","last_updated":"2026-11-07T01:52:00Z","images":[]},
					{"name":"2.1.2-alpine","digest":"sha256:mosq212","last_updated":"2026-11-07T01:52:00Z","images":[]},
					{"name":"2.1-alpine","digest":"sha256:mosq212","last_updated":"2026-11-07T01:52:00Z","images":[]},
					{"name":"2","digest":"sha256:mosq212","last_updated":"2026-11-07T01:52:00Z","images":[]},
					{"name":"2.0.22-openssl","digest":"sha256:mosq2022ssl","last_updated":"2026-11-06T01:00:00Z","images":[]},
					{"name":"2.0.22","digest":"sha256:mosq2022","last_updated":"2026-11-06T01:00:00Z","images":[]},
					{"name":"2.1.1-alpine","digest":"sha256:mosq211","last_updated":"2026-10-01T01:00:00Z","images":[]},
					{"name":"2.0.21","digest":"sha256:mosq2021","last_updated":"2026-09-01T01:00:00Z","images":[]}
				]
			}`))
		case strings.HasPrefix(r.URL.Path, "/v2/repositories/library/mysql/tags"):
			_, _ = w.Write([]byte(`{
				"next": "",
				"results": [
					{"name":"oraclelinux9","digest":"sha256:m26","last_updated":"2026-11-10T10:00:00Z","images":[]},
					{"name":"26.10.0","digest":"sha256:m26","last_updated":"2026-11-10T10:00:00Z","images":[]},
					{"name":"8.4.13-oracle","digest":"sha256:m8413","last_updated":"2026-11-09T10:00:00Z","images":[]},
					{"name":"8.4.13","digest":"sha256:m8413","last_updated":"2026-11-09T10:00:00Z","images":[]},
					{"name":"8.4","digest":"sha256:m8413","last_updated":"2026-11-09T10:00:00Z","images":[]},
					{"name":"8.4.12","digest":"sha256:m8412","last_updated":"2026-10-09T10:00:00Z","images":[]}
				]
			}`))
		case strings.HasPrefix(r.URL.Path, "/v2/repositories/lejianwen/rustdesk-server-s6/tags"):
			_, _ = w.Write([]byte(`{
				"next": "",
				"results": [
					{"name":"v0.1.2-armv7","digest":"sha256:arm","last_updated":"2026-11-10T10:00:00Z","images":[]},
					{"name":"v0.1.2","digest":"sha256:rd012","last_updated":"2026-11-10T09:00:00Z","images":[]},
					{"name":"v0.0.9-f3","digest":"sha256:rd009","last_updated":"2026-10-10T09:00:00Z","images":[]}
				]
			}`))
		case r.URL.Path == "/v2/su-kaka/gcli2api/tags/list":
			_, _ = w.Write([]byte(`{"tags":["sha-faead14","master-dd1f3fc","master-ecc207f","sha-1111111"]}`))
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

	// Case 1: mosquitto:2 must NOT surface 1.6.15-openssl at #1; must keep 2, 2.1.2-alpine, 2.1.1-alpine, 2.0.22, 2.0.21
	refMosq, _ := version.ParseImageRef("eclipse-mosquitto:2")
	mosqItems, mosqHi, err := cli.SearchTags(ctx, refMosq, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if mosqHi != "2.1.2-alpine" {
		t.Fatalf("expected mosquitto highestTag=2.1.2-alpine, got %q", mosqHi)
	}
	if len(mosqItems) < 5 || mosqItems[0].Name != "2" || mosqItems[1].Name != "2.1.2-alpine" || mosqItems[2].Name != "2.1.1-alpine" || mosqItems[3].Name != "2.0.22" {
		t.Fatalf("unexpected mosquitto order: %+v", mosqItems)
	}
	for _, it := range mosqItems {
		if strings.Contains(it.Name, "openssl") {
			t.Fatalf("unexpected openssl variant leaked into default list: %q", it.Name)
		}
	}

	// Case 2: mysql:8.4 must scope highestTag to 8.4.13 (not 26.10.0) and list 8.4, 8.4.13, 8.4.12 first
	refMysql, _ := version.ParseImageRef("mysql:8.4")
	mysqlItems, mysqlHi, err := cli.SearchTags(ctx, refMysql, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if mysqlHi != "8.4.13" {
		t.Fatalf("expected mysql:8.4 scoped highestTag=8.4.13, got %q", mysqlHi)
	}
	if len(mysqlItems) < 3 || mysqlItems[0].Name != "8.4" || mysqlItems[1].Name != "8.4.13" || mysqlItems[2].Name != "8.4.12" {
		t.Fatalf("unexpected mysql:8.4 order: %+v", mysqlItems)
	}

	// Case 3: rustdesk v0.0.9-f3 revision suffix must NOT hide v0.1.2, and -armv7 must be filtered
	refRd, _ := version.ParseImageRef("lejianwen/rustdesk-server-s6:v0.0.9-f3")
	rdItems, rdHi, err := cli.SearchTags(ctx, refRd, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if rdHi != "v0.1.2" || len(rdItems) != 2 || rdItems[0].Name != "v0.1.2" {
		t.Fatalf("unexpected rustdesk items: hi=%q items=%+v", rdHi, rdItems)
	}

	// Case 4: non-SemVer repo gcli2api:master-ecc207f pins current tag first and sets highestTag to current tag
	refGcli, _ := version.ParseImageRef("ghcr.io/su-kaka/gcli2api:master-ecc207f")
	gcliItems, gcliHi, err := cli.SearchTags(ctx, refGcli, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if gcliHi != "master-ecc207f" || len(gcliItems) != 4 || gcliItems[0].Name != "master-ecc207f" || gcliItems[1].Name != "sha-faead14" {
		t.Fatalf("unexpected non-semver items: hi=%q items=%+v", gcliHi, gcliItems)
	}
}

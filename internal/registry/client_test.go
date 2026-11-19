package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

	var regGetCalls atomic.Int32
	var ociHeadCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/repositories/library/eclipse-mosquitto/tags/2":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"digest": "sha256:hubindex999",
				"images": [{"os":"linux","architecture":"amd64","digest":"sha256:hubamd64888"}]
			}`))
		case r.URL.Path == "/v2/library/eclipse-mosquitto/manifests/2":
			regGetCalls.Add(1)
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
			ociHeadCalls.Add(1)
			w.Header().Set("Docker-Content-Digest", "sha256:ocidate333")
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v2/org/custom/manifests/20261101-1" && r.Method == http.MethodHead:
			ociHeadCalls.Add(1)
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
	if regGetCalls.Load() != 0 || mf.RepoDigest != "sha256:hubindex999" || mf.PlatformDigest != "sha256:hubamd64888" {
		t.Fatalf("expected 0 registry GET calls and valid digests, got calls=%d mf=%+v", regGetCalls.Load(), mf)
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
	if len(matchedOCI) != 1 || matchedOCI[0] != "20261110-3" || ociHeadCalls.Load() == 0 {
		t.Fatalf("expected OCI date alias 20261110-3 via HEAD, got matched=%v headCalls=%d", matchedOCI, ociHeadCalls.Load())
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

	// Case 1: mosquitto:2 pins latest at #0, current floating tag 2 at #1, then 2.1.2-alpine, 2.1.1-alpine, 2.0.22
	refMosq, _ := version.ParseImageRef("eclipse-mosquitto:2")
	mosqItems, mosqHi, err := cli.SearchTags(ctx, refMosq, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if mosqHi != "2.1.2-alpine" {
		t.Fatalf("expected mosquitto highestTag=2.1.2-alpine, got %q", mosqHi)
	}
	if len(mosqItems) < 5 || mosqItems[0].Name != "latest" || mosqItems[1].Name != "2" || mosqItems[2].Name != "2.1.2-alpine" || mosqItems[3].Name != "2.1.1-alpine" || mosqItems[4].Name != "2.0.22" {
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

func TestFetchImageLayerSizes_HubAndOCIMultiArch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _ := secret.LoadOrCreate(dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/repositories/homeassistant/home-assistant/tags/latest/images":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"os":"linux","architecture":"arm64","layers":[{"digest":"sha256:aaaa111122223333444455556666777788889999000011112222333344445555","size":12000}]},
				{"os":"linux","architecture":"amd64","layers":[
					{"digest":"","size":0},
					{"digest":"sha256:6f837d52a1b90000111122223333444455556666777788889999000011112222","size":38500000},
					{"digest":"sha256:b2c3d4e5f6a10000111122223333444455556666777788889999000011112222","size":90500000}
				]}
			]`))
		case "/v2/org/app/manifests/v1.2.0":
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			_, _ = w.Write([]byte(`{
				"manifests":[
					{"digest":"sha256:subamd6411111111111111111111111111111111111111111111111111111111","platform":{"os":"linux","architecture":"amd64"}}
				]
			}`))
		case "/v2/org/app/manifests/sha256:subamd6411111111111111111111111111111111111111111111111111111111":
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			_, _ = w.Write([]byte(`{
				"layers":[
					{"digest":"sha256:deadbeefcafebabe111122223333444455556666777788889999000011112222","size":77000000}
				]
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cli := New(st, box)
	cli.BaseURLOverride = map[string]string{
		"hub.docker.com": srv.URL,
		"ghcr.io":        srv.URL,
	}

	hubSizes := cli.FetchImageLayerSizes(ctx, "homeassistant/home-assistant:latest", "linux/amd64")
	if hubSizes["6f837d52a1b9"] != 38500000 || hubSizes["b2c3d4e5f6a1"] != 90500000 {
		t.Fatalf("unexpected hub layer sizes: %+v", hubSizes)
	}

	ociSizes := cli.FetchImageLayerSizes(ctx, "ghcr.io/org/app:v1.2.0", "linux/amd64")
	if ociSizes["deadbeefcafe"] != 77000000 {
		t.Fatalf("unexpected oci multi-arch layer sizes: %+v", ociSizes)
	}
}

func TestSearchTagsExPrereleasePrefixSortAndRemoteRefresh(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _ := secret.LoadOrCreate(dir)

	ghcrVersion := 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v2/repositories/louislam/uptime-kuma/tags"):
			_, _ = w.Write([]byte(`{
				"next": "",
				"results": [
					{"name":"3.0.0-beta.1-slim-rootless","digest":"sha256:b1sr","last_updated":"2026-11-17T16:10:58Z","images":[]},
					{"name":"3.0.0-beta.1-rootless","digest":"sha256:b1r","last_updated":"2026-11-17T16:10:51Z","images":[]},
					{"name":"3.0.0-beta.1-slim","digest":"sha256:b1s","last_updated":"2026-11-17T16:10:39Z","images":[]},
					{"name":"3.0.0-beta.1","digest":"sha256:b1","last_updated":"2026-11-17T15:51:31Z","images":[]},
					{"name":"beta","digest":"sha256:b1","last_updated":"2026-11-17T15:51:28Z","images":[]},
					{"name":"latest","digest":"sha256:u2512","last_updated":"2026-11-17T15:23:08Z","images":[]},
					{"name":"2.5.12","digest":"sha256:u2512","last_updated":"2026-11-17T15:23:08Z","images":[]},
					{"name":"2.5.12-rootless","digest":"sha256:u2512r","last_updated":"2026-11-17T15:22:20Z","images":[]},
					{"name":"2.5.11","digest":"sha256:u2511","last_updated":"2026-11-02T10:00:00Z","images":[]},
					{"name":"2.3.0","digest":"sha256:u230","last_updated":"2026-05-01T03:52:38Z","images":[]},
					{"name":"1.23.9","digest":"sha256:u1239","last_updated":"2026-11-17T16:20:00Z","images":[]},
					{"name":"1.23.0","digest":"sha256:u1230","last_updated":"2023-08-17T14:38:00Z","images":[]}
				]
			}`))
		case r.URL.Path == "/v2/louislam/uptime-kuma/tags/list":
			if ghcrVersion == 1 {
				_, _ = w.Write([]byte(`{"tags":["latest","2.5.11"]}`))
			} else {
				_, _ = w.Write([]byte(`{"tags":["latest","2.5.11","2.5.12","3.0.0-beta.1"]}`))
			}
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

	refStable, _ := version.ParseImageRef("louislam/uptime-kuma:2.5.11")

	// 1. Default list on stable container: latest is #0 (aliasOf=2.5.12), prerelease hidden by default, -rootless filtered
	resDefault, err := cli.SearchTagsEx(ctx, refStable, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if resDefault.CurrentPrerelease || !resDefault.HasTimestamps || resDefault.HighestTag != "2.5.12" {
		t.Fatalf("unexpected default meta: %+v", resDefault)
	}
	if len(resDefault.Items) < 2 || resDefault.Items[0].Name != "latest" || resDefault.Items[0].AliasOf != "2.5.12" || resDefault.Items[1].Name != "2.5.12" {
		t.Fatalf("expected latest(2.5.12) at #0 and 2.5.12 at #1, got %+v", resDefault.Items)
	}
	for _, it := range resDefault.Items {
		if it.Prerelease || strings.Contains(it.Name, "rootless") {
			t.Fatalf("unexpected item in clean stable list: %+v", it)
		}
	}

	// 2. Enabling IncludePrerelease reveals beta(3.0.0-beta.1) and 3.0.0-beta.1 without leaking -slim/-rootless
	incTrue := true
	resPre, err := cli.SearchTagsEx(ctx, refStable, SearchOptions{IncludePrerelease: &incTrue})
	if err != nil {
		t.Fatal(err)
	}
	if resPre.HighestTag != "3.0.0-beta.1" || resPre.HighestStableTag != "2.5.12" {
		t.Fatalf("expected highest=3.0.0-beta.1 and highestStable=2.5.12, got %+v", resPre)
	}
	if resPre.Items[0].Name != "latest" || resPre.Items[1].Name != "beta" || resPre.Items[1].AliasOf != "3.0.0-beta.1" || resPre.Items[2].Name != "3.0.0-beta.1" {
		t.Fatalf("unexpected prerelease-enabled order: %+v", resPre.Items)
	}

	// 3. Searching "3.0" puts 3.0.0-beta.1 at #0 ahead of 2.3.0 and 1.23.0 even when IncludePrerelease is not set
	resSearch, err := cli.SearchTagsEx(ctx, refStable, SearchOptions{Query: "3.0", Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(resSearch.Items) < 5 || resSearch.Items[0].Name != "3.0.0-beta.1" || !resSearch.Items[0].Prerelease {
		t.Fatalf("expected 3.0.0-beta.1 at #0 when searching 3.0, got %+v", resSearch.Items)
	}

	// 4. SortBy="time" puts 1.23.9 (newest timestamp 16:20:00Z) at #0
	resTime, err := cli.SearchTagsEx(ctx, refStable, SearchOptions{SortBy: "time"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resTime.Items) == 0 || resTime.Items[0].Name != "1.23.9" {
		t.Fatalf("expected newest timestamp 1.23.9 at #0 in time mode, got %+v", resTime.Items)
	}

	// 5. Container already on :beta defaults CurrentPrerelease=true and includes 3.0.0-beta.1 automatically
	refBeta, _ := version.ParseImageRef("louislam/uptime-kuma:beta")
	resOnBeta, err := cli.SearchTagsEx(ctx, refBeta, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !resOnBeta.CurrentPrerelease || resOnBeta.HighestTag != "3.0.0-beta.1" {
		t.Fatalf("expected :beta container to auto-enable prerelease, got %+v", resOnBeta)
	}

	// 6. Non-DockerHub OCI registry (ghcr.io) Remote=true bypasses 6h cache and picks up newly pushed tags
	refGHCR, _ := version.ParseImageRef("ghcr.io/louislam/uptime-kuma:2.5.11")
	r1, err := cli.SearchTagsEx(ctx, refGHCR, SearchOptions{Remote: false})
	if err != nil || r1.HighestTag != "2.5.11" || r1.HasTimestamps {
		t.Fatalf("unexpected initial ghcr result: err=%v res=%+v", err, r1)
	}
	ghcrVersion = 2
	r2Cached, _ := cli.SearchTagsEx(ctx, refGHCR, SearchOptions{Remote: false})
	if r2Cached.HighestTag != "2.5.11" {
		t.Fatalf("expected cached ghcr result before remote refresh, got %+v", r2Cached)
	}
	r2Fresh, err := cli.SearchTagsEx(ctx, refGHCR, SearchOptions{Query: "3.0", Remote: true})
	if err != nil || len(r2Fresh.Items) == 0 || r2Fresh.Items[0].Name != "3.0.0-beta.1" || r2Fresh.HighestStableTag != "2.5.12" {
		t.Fatalf("expected ghcr Remote=true to bypass cache and find 3.0.0-beta.1, got err=%v res=%+v", err, r2Fresh)
	}
}

func TestCheckSessionDeduplicatesBypassCacheAndBearerTokenFlight(t *testing.T) {
	ctx := WithCheckSession(context.Background())
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _ := secret.LoadOrCreate(dir)

	var (
		tokenCalls atomic.Int32
		tagsCalls  atomic.Int32
		srvURL     string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			tokenCalls.Add(1)
			time.Sleep(25 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"shared-jwt"}`))
		case r.URL.Path == "/v2/org/svc/tags/list":
			if r.Header.Get("Authorization") != "Bearer shared-jwt" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="ghcr.io",scope="repository:org/svc:pull"`, srvURL))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			tagsCalls.Add(1)
			time.Sleep(25 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tags":["latest","1.0.0","1.1.0","1.2.0","1.3.0"]}`))
		case strings.HasPrefix(r.URL.Path, "/v2/org/svc/manifests/"):
			if r.Header.Get("Authorization") != "Bearer shared-jwt" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="ghcr.io",scope="repository:org/svc:pull"`, srvURL))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			tag := strings.TrimPrefix(r.URL.Path, "/v2/org/svc/manifests/")
			if tag == "1.3.0" || tag == "latest" {
				w.Header().Set("Docker-Content-Digest", "sha256:target130")
			} else {
				w.Header().Set("Docker-Content-Digest", "sha256:older")
			}
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"config":{"digest":"sha256:cfg130"}}`))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	cli := New(st, box)
	cli.BaseURLOverride = map[string]string{"ghcr.io": srv.URL}

	// 6 concurrent callers checking same repo with bypassCache=true in one check session
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tag := "1.1.0"
			if idx%2 == 0 {
				tag = "1.2.0"
			}
			ref, _ := version.ParseImageRef("ghcr.io/org/svc:" + tag)
			if _, mErr := cli.CheckManifestEx(ctx, ref, ManifestCheckOptions{HostPlatform: "linux/amd64", BypassCache: true}); mErr != nil {
				t.Errorf("CheckManifestEx: %v", mErr)
			}
			tags, tErr := cli.FetchRecentTags(ctx, ref, 3, true)
			if tErr != nil || len(tags) != 5 {
				t.Errorf("FetchRecentTags: err=%v len=%d", tErr, len(tags))
			}
			matched := cli.LookupTagsByDigest(ctx, ref, "sha256:target130", "", "linux/amd64", tags)
			if len(matched) == 0 || matched[0] != "1.3.0" {
				t.Errorf("LookupTagsByDigest matched=%v, want [1.3.0]", matched)
			}
		}(i)
	}
	wg.Wait()

	if tokenCalls.Load() != 1 {
		t.Fatalf("expected exactly 1 /token request via flight dedup, got %d", tokenCalls.Load())
	}
	if tagsCalls.Load() != 1 {
		t.Fatalf("expected exactly 1 /tags/list request in check session even with bypassCache=true, got %d", tagsCalls.Load())
	}
}

func TestSearchTagsExFullModalPoolAndConcurrentPages(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _ := secret.LoadOrCreate(dir)

	var hubFail atomic.Bool
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hubFail.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if r.URL.Path != "/v2/repositories/louislam/uptime-kuma/tags" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		page := r.URL.Query().Get("page")
		switch page {
		case "", "1":
			_, _ = w.Write([]byte(fmt.Sprintf(`{
				"count": 122,
				"next": "%s/v2/repositories/louislam/uptime-kuma/tags?page=2&page_size=100",
				"results": [
					{"name":"latest","digest":"sha256:u2512","last_updated":"2026-11-17T15:23:08Z","images":[]},
					{"name":"beta","digest":"sha256:b1","last_updated":"2026-11-18T10:00:00Z","images":[]},
					{"name":"3.0.0-beta.1","digest":"sha256:b1","last_updated":"2026-11-18T10:00:00Z","images":[]},
					{"name":"3.0.0-beta.1-slim","digest":"sha256:b1s","last_updated":"2026-11-18T10:01:00Z","images":[]},
					{"name":"2.5.12","digest":"sha256:u2512","last_updated":"2026-11-17T15:23:08Z","images":[]},
					{"name":"2.5.12-slim","digest":"sha256:u2512s","last_updated":"2026-11-17T15:24:00Z","images":[]},
					{"name":"2.5","digest":"sha256:u2512","last_updated":"2026-11-17T15:23:08Z","images":[]}
				]
			}`, srvURL)))
		case "2":
			var buf strings.Builder
			buf.WriteString(`{"count":122,"next":"","results":[`)
			for i := 1; i <= 55; i++ {
				if i > 1 {
					buf.WriteByte(',')
				}
				buf.WriteString(fmt.Sprintf(`{"name":"1.23.%d","digest":"sha256:old%d","last_updated":"2025-01-01T00:00:00Z","images":[]}`, i, i))
			}
			buf.WriteString(`]}`)
			_, _ = w.Write([]byte(buf.String()))
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

	ref, _ := version.ParseImageRef("louislam/uptime-kuma:2.5.11")
	res, err := cli.SearchTagsEx(ctx, ref, SearchOptions{All: true, Remote: true})
	if err != nil {
		t.Fatalf("SearchTagsEx All=true failed: %v", err)
	}
	// 全量标签（7 + 55 = 62 > 50）全部返回，绝不丢弃 -slim / 2.5 / beta，也不截断 50 条
	if len(res.Items) != 62 {
		t.Fatalf("expected full 62 items without 50-item truncation or variant dropping, got %d", len(res.Items))
	}
	if res.HighestStableTag != "2.5.12" || res.HighestPrereleaseTag != "3.0.0-beta.1" {
		t.Fatalf("unexpected highest tags: stable=%q pre=%q", res.HighestStableTag, res.HighestPrereleaseTag)
	}

	var stableList, preList []string
	hasSlim := false
	hasShort := false
	for _, it := range res.Items {
		if it.Name == "2.5.12-slim" {
			hasSlim = true
		}
		if it.Name == "2.5" && it.AliasOf == "2.5.12" {
			hasShort = true
		}
		if it.StableVisible {
			stableList = append(stableList, it.Name)
		}
		if it.PreVisible {
			preList = append(preList, it.Name)
		}
	}
	if !hasSlim || !hasShort {
		t.Fatalf("expected variant 2.5.12-slim and floating 2.5(2.5.12) preserved in full pool: hasSlim=%v hasShort=%v", hasSlim, hasShort)
	}
	if len(stableList) < 3 || stableList[0] != "latest" || stableList[1] != "2.5.12" {
		t.Fatalf("unexpected stableVisible head: %v", stableList[:minInt(5, len(stableList))])
	}
	if len(preList) < 4 || preList[0] != "latest" || preList[1] != "beta" || preList[2] != "3.0.0-beta.1" || preList[3] != "2.5.12" {
		t.Fatalf("unexpected preVisible head: %v", preList[:minInt(5, len(preList))])
	}

	// 网络异常时自动回退本地已缓存的全量标签池
	hubFail.Store(true)
	resFallback, err := cli.SearchTagsEx(ctx, ref, SearchOptions{All: true, Remote: true})
	if err != nil || len(resFallback.Items) != 62 {
		t.Fatalf("expected fallback to full cached 62 items on 429, got err=%v len=%d", err, len(resFallback.Items))
	}
}

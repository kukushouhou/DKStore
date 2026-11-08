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

package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kukushouhou/DKStore/internal/auth"
	"github.com/kukushouhou/DKStore/internal/checker"
	"github.com/kukushouhou/DKStore/internal/host"
	"github.com/kukushouhou/DKStore/internal/hostauth"
	"github.com/kukushouhou/DKStore/internal/ignore"
	"github.com/kukushouhou/DKStore/internal/inventory"
	"github.com/kukushouhou/DKStore/internal/notify"
	"github.com/kukushouhou/DKStore/internal/registry"
	"github.com/kukushouhou/DKStore/internal/secret"
	"github.com/kukushouhou/DKStore/internal/store"
	"github.com/kukushouhou/DKStore/internal/updater"
)

func TestAPIEndpoints_IconsTagsIgnoreAndCustomUpdateValidation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _ := secret.LoadOrCreate(dir)

	pngBytes := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	hubHits := 0
	mockUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/media/repos_logo/v1/"):
			hubHits++
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
		case r.URL.Path == "/v2/repositories/library/nginx/tags":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"next":"","results":[{"name":"1.27.3","digest":"sha256:aaa","last_updated":"2026-11-10T10:00:00Z","images":[]},{"name":"1.26.2","digest":"sha256:bbb","last_updated":"2026-10-01T10:00:00Z","images":[]}]}`))
		case r.URL.Path == "/v2/repositories/library/nginx/tags/99.99.99", r.URL.Path == "/v2/library/nginx/manifests/99.99.99":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockUp.Close()

	authSvc := auth.New(st, nil)
	hostReg := host.NewRegistry(st, box)
	invSvc := inventory.New(st, hostReg)
	regCli := registry.New(st, box)
	regCli.BaseURLOverride = map[string]string{
		"docker.io":      mockUp.URL,
		"hub.docker.com": mockUp.URL,
		"github.com":     mockUp.URL,
	}
	ignSvc := ignore.New(st)
	ntfSvc := notify.New(st)
	chkSvc := checker.New(slog.Default(), st, hostReg, invSvc, regCli, ignSvc, ntfSvc)
	upSvc := updater.New(slog.Default(), st, hostReg, invSvc, regCli)

	srv := New(Deps{
		Log:     slog.Default(),
		Store:   st,
		Auth:    authSvc,
		Hosts:   hostReg,
		Reg:     regCli,
		Ignore:  ignSvc,
		Notify:  ntfSvc,
		Checker: chkSvc,
		Updater: upSvc,
	}).Handler()

	// Setup password and capture session cookie
	setupRec := httptest.NewRecorder()
	setupReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", strings.NewReader(`{"password":"password123"}`))
	srv.ServeHTTP(setupRec, setupReq)
	if setupRec.Code != http.StatusOK {
		t.Fatalf("setup status=%d body=%s", setupRec.Code, setupRec.Body.String())
	}
	cookies := setupRec.Result().Cookies()

	now := time.Now().Unix()
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO host(id, name, kind, transport, status, created_at) VALUES ('h1', 'Local', 'docker', 'local', 'online', ?)`, now)
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO app(key, host_id, name, source, updated_at) VALUES ('h1/container/web', 'h1', 'web', 'standalone', ?)`, now)
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO service(key, app_key, name, state, image_ref, image_id, repo_digest, platform, updated_at) VALUES ('h1/container/web/main', 'h1/container/web', 'web', 'running', 'nginx:1.26.2', 'sha256:bbb', 'sha256:bbb', 'linux/amd64', ?)`, now)

	doReq := func(method, target, body string) *httptest.ResponseRecorder {
		var rBody *bytes.Reader
		if body != "" {
			rBody = bytes.NewReader([]byte(body))
		} else {
			rBody = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, target, rBody)
		for _, ck := range cookies {
			req.AddCookie(ck)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	// 1. GET /api/v1/icons fetches and caches in SQLite (second call hits cache, hubHits stays 1)
	recIcon1 := doReq(http.MethodGet, "/api/v1/icons?serviceKey=h1%2Fcontainer%2Fweb%2Fmain", "")
	recIcon2 := doReq(http.MethodGet, "/api/v1/icons?serviceKey=h1%2Fcontainer%2Fweb%2Fmain", "")
	if recIcon1.Code != http.StatusOK || recIcon2.Code != http.StatusOK || hubHits != 1 {
		t.Fatalf("icon proxy failed: code1=%d code2=%d hubHits=%d", recIcon1.Code, recIcon2.Code, hubHits)
	}

	// 2. GET /api/v1/services/tags
	recTags := doReq(http.MethodGet, "/api/v1/services/tags?serviceKey=h1%2Fcontainer%2Fweb%2Fmain&q=1.27&remote=0", "")
	if recTags.Code != http.StatusOK || !strings.Contains(recTags.Body.String(), `"highestTag":"1.27.3"`) {
		t.Fatalf("tags search failed: code=%d body=%s", recTags.Code, recTags.Body.String())
	}

	// 3. POST /api/v1/ignore with prefix lock
	recIgn := doReq(http.MethodPost, "/api/v1/ignore", `{"serviceKeys":["h1/container/web/main"],"mode":"prefix","prefix":"1.26"}`)
	if recIgn.Code != http.StatusOK {
		t.Fatalf("set prefix ignore failed: %d %s", recIgn.Code, recIgn.Body.String())
	}

	// 4. POST /api/v1/updates with non-existent custom targetTag returns localized 400 error
	recUpd := doReq(http.MethodPost, "/api/v1/updates", `{"items":[{"serviceKey":"h1/container/web/main","targetTag":"99.99.99"}]}`)
	if recUpd.Code != http.StatusBadRequest || !strings.Contains(recUpd.Body.String(), "99.99.99") {
		t.Fatalf("expected localized tag-not-found error, got code=%d body=%s", recUpd.Code, recUpd.Body.String())
	}

	// 5. GET & PUT /api/v1/settings supports progressStyle (default "layered") and partial/full preservation
	recSet0 := doReq(http.MethodGet, "/api/v1/settings", "")
	if recSet0.Code != http.StatusOK || !strings.Contains(recSet0.Body.String(), `"progressStyle":"layered"`) {
		t.Fatalf("expected default progressStyle=layered, got %d %s", recSet0.Code, recSet0.Body.String())
	}
	recSet1 := doReq(http.MethodPut, "/api/v1/settings", `{"locale":"en-US","checkIntervalHours":12,"channel":"webhook","webhookUrl":"https://example.com/hook"}`)
	if recSet1.Code != http.StatusOK {
		t.Fatalf("save full settings failed: %d %s", recSet1.Code, recSet1.Body.String())
	}
	// Partial update of progressStyle must NOT wipe webhookUrl or checkIntervalHours
	recSet2 := doReq(http.MethodPut, "/api/v1/settings", `{"progressStyle":"unified"}`)
	if recSet2.Code != http.StatusOK || !strings.Contains(recSet2.Body.String(), `"progressStyle":"unified"`) || !strings.Contains(recSet2.Body.String(), `"checkIntervalHours":12`) || !strings.Contains(recSet2.Body.String(), `"https://example.com/hook"`) {
		t.Fatalf("partial progressStyle save clobbered settings: %d %s", recSet2.Code, recSet2.Body.String())
	}
	// Full update without progressStyle must preserve "unified"
	recSet3 := doReq(http.MethodPut, "/api/v1/settings", `{"locale":"zh-CN","checkIntervalHours":6}`)
	if recSet3.Code != http.StatusOK || !strings.Contains(recSet3.Body.String(), `"progressStyle":"unified"`) {
		t.Fatalf("full settings save lost progressStyle: %d %s", recSet3.Code, recSet3.Body.String())
	}

	// 6. POST /api/v1/apps/compose-update launches tracked Job and applies policyMode
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO app(key, host_id, name, source, project_json, updated_at) VALUES ('h1/compose/stack', 'h1', 'stack', 'compose', '{"name":"stack","workingDir":"/tmp/nonexistent"}', ?)`, now)
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO service(key, app_key, name, state, image_ref, image_id, repo_digest, platform, updated_at) VALUES ('h1/compose/stack/api', 'h1/compose/stack', 'api', 'running', 'nginx:1.26.2', 'sha256:bbb', 'sha256:bbb', 'linux/amd64', ?)`, now)

	recComp := doReq(http.MethodPost, "/api/v1/apps/compose-update", `{"appKey":"h1/compose/stack","serviceKey":"h1/compose/stack/api","policyMode":"prefix","policyPrefix":"1.27","content":"services:\n  api:\n    image: nginx:1.27.3\n"}`)
	if recComp.Code != http.StatusOK || !strings.Contains(recComp.Body.String(), `"job":{`) {
		t.Fatalf("expected compose-update to return tracked job, got %d %s", recComp.Code, recComp.Body.String())
	}

	// 7. GET /api/v1/services/config & POST /api/v1/services/config-update
	recCfgNonSA := doReq(http.MethodGet, "/api/v1/services/config?serviceKey=h1%2Fcompose%2Fstack%2Fapi", "")
	if recCfgNonSA.Code != http.StatusOK || !strings.Contains(recCfgNonSA.Body.String(), `"editable":false`) {
		t.Fatalf("expected non-standalone service config editable=false, got %d %s", recCfgNonSA.Code, recCfgNonSA.Body.String())
	}
	recCfgUpd := doReq(http.MethodPost, "/api/v1/services/config-update", `{"serviceKey":"h1/container/web/main","policyMode":"forever","env":[{"key":"TZ","value":"UTC"}],"ports":[{"hostPort":"8080","containerPort":"80","protocol":"tcp"}]}`)
	if recCfgUpd.Code != http.StatusOK || !strings.Contains(recCfgUpd.Body.String(), `"job":{`) {
		t.Fatalf("expected services/config-update to return tracked job, got %d %s", recCfgUpd.Code, recCfgUpd.Body.String())
	}

	// 8. POST /api/v1/auth/device-ticket MUST reject untrusted TCP requests (HTTP 403), and succeed on trusted gateway context
	recUntrustedTicket := doReq(http.MethodPost, "/api/v1/auth/device-ticket", "{}")
	if recUntrustedTicket.Code != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 for untrusted TCP device-ticket request, got %d", recUntrustedTicket.Code)
	}
	trustedReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/device-ticket", strings.NewReader("{}"))
	trustedReq = trustedReq.WithContext(hostauth.WithTrustedGateway(trustedReq.Context()))
	recTrustedTicket := httptest.NewRecorder()
	srv.ServeHTTP(recTrustedTicket, trustedReq)
	if recTrustedTicket.Code != http.StatusOK || !strings.Contains(recTrustedTicket.Body.String(), `"ticket":"`) {
		t.Fatalf("expected HTTP 200 ticket on trusted gateway context, got %d %s", recTrustedTicket.Code, recTrustedTicket.Body.String())
	}

	// 9. GET /api/v1/hosts/metrics?cached=1 returns persisted SQLite host metrics snapshot
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO host_metrics_snapshot(host_id, metrics_json, updated_at) VALUES ('h1', '{"hostId":"h1","online":true,"cpuPercent":21.5,"cpuCores":4,"memUsedBytes":4294967296,"memTotalBytes":17179869184,"disks":[{"mount":"/volume1","totalBytes":1099511627776,"freeBytes":549755813888,"usedBytes":549755813888}]}', ?)`, time.Now().Unix())
	recMetricsCached := doReq(http.MethodGet, "/api/v1/hosts/metrics?cached=1", "")
	if recMetricsCached.Code != http.StatusOK || !strings.Contains(recMetricsCached.Body.String(), `"cpuPercent":21.5`) || !strings.Contains(recMetricsCached.Body.String(), `"/volume1"`) {
		t.Fatalf("expected cached host metrics snapshot, got %d %s", recMetricsCached.Code, recMetricsCached.Body.String())
	}

	// Wait briefly for background goroutines launched against mock local socket to settle before TempDir cleanup
	time.Sleep(150 * time.Millisecond)
}

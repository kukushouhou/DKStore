package transport

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/platform"
)

// MirrorConfigTransport 扩展 ExecutableTransport，提供宿主机 Docker 配置热重载与从目标设备网络发起 HTTP/Registry 请求的能力。
type MirrorConfigTransport interface {
	ExecutableTransport
	ReloadDockerDaemon(ctx context.Context) error
	NewHostHTTPClient(ctx context.Context) (*http.Client, func())
	ProbeMirrorURLFromHost(ctx context.Context, mirrorURL string) (ok bool, latencyMs int64, rawErr string)
}

const reloadDockerScript = `PATH="$PATH:/usr/local/bin:/usr/sbin:/sbin:/bin:/usr/bin"; ` +
	`if command -v systemctl >/dev/null 2>&1 && systemctl reload docker >/dev/null 2>&1; then exit 0; fi; ` +
	`if command -v pidof >/dev/null 2>&1; then pids=$(pidof dockerd 2>/dev/null || true); if [ -n "$pids" ]; then kill -HUP $pids && exit 0; fi; fi; ` +
	`if command -v pkill >/dev/null 2>&1 && pkill -HUP -x dockerd >/dev/null 2>&1; then exit 0; fi; ` +
	`if [ -f /var/run/docker.pid ]; then kill -HUP "$(cat /var/run/docker.pid)" && exit 0; fi; ` +
	`exit 1`

// ReloadDockerDaemon 在本机通过 SIGHUP 热重载 dockerd 配置（不中断运行中容器）。
func (l *LocalTransport) ReloadDockerDaemon(ctx context.Context) error {
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(rctx, "sh", "-c", reloadDockerScript)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("reload docker daemon: %w (%s)", err, msg)
		}
		return fmt.Errorf("reload docker daemon: %w", err)
	}
	return nil
}

// ReloadDockerDaemon 在远端 SSH 主机上通过 SIGHUP 热重载 dockerd 配置（不中断运行中容器）。
func (t *SSHTransport) ReloadDockerDaemon(ctx context.Context) error {
	if err := t.ensureClient(ctx); err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	out, err := t.runPrivilegedCmd(rctx, "sh -c "+shellQuote(reloadDockerScript), nil)
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("reload docker daemon: %w (%s)", err, msg)
		}
		return fmt.Errorf("reload docker daemon: %w", err)
	}
	return nil
}

// ProbeMirrorURLFromHost 在本机直接向 <mirrorURL>/v2/ 发起连通与 OCI V2 响应核验。
func (l *LocalTransport) ProbeMirrorURLFromHost(ctx context.Context, mirrorURL string) (bool, int64, string) {
	return ProbeMirrorViaGoHTTP(ctx, mirrorURL)
}

// ProbeMirrorURLFromHost 在远端 SSH 主机上复用 dockerd 的代理环境向 <mirrorURL>/v2/ 发起探测。
func (t *SSHTransport) ProbeMirrorURLFromHost(ctx context.Context, mirrorURL string) (bool, int64, string) {
	clean := strings.TrimRight(strings.TrimSpace(mirrorURL), "/")
	if clean == "" {
		return false, 0, "invalid url"
	}
	if err := t.ensureClient(ctx); err != nil {
		return false, 0, err.Error()
	}
	targetV2 := clean + "/v2/"
	// 自动加载目标主机 docker.service 配置的 HTTP_PROXY / HTTPS_PROXY / NO_PROXY，确保与 dockerd 真实拉取链路一致
	script := fmt.Sprintf(
		`PATH="$PATH:/usr/local/bin:/usr/bin:/bin"; `+
			`if command -v systemctl >/dev/null 2>&1; then `+
			`  eval "$(systemctl show -p Environment docker 2>/dev/null | sed 's/^Environment=//')" 2>/dev/null || true; `+
			`  export HTTP_PROXY HTTPS_PROXY NO_PROXY http_proxy https_proxy no_proxy; `+
			`fi; `+
			`if command -v curl >/dev/null 2>&1; then `+
			`  curl -sS -k -D - -o /dev/null -w '\n__DK_PROBE__:%s' --connect-timeout 5 --max-time 7 %s 2>&1; `+
			`else `+
			`  echo '__DK_NO_CURL__'; `+
			`fi`,
		"%{http_code}:%{time_total}",
		shellQuote(targetV2),
	)
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := time.Now()
	out, err := t.runSession(pctx, "sh -c "+shellQuote(script), nil, 64<<10, true)
	raw := string(out)
	if strings.Contains(raw, "__DK_NO_CURL__") {
		return ProbeMirrorViaGoHTTP(ctx, clean)
	}
	return parseCurlMirrorProbeOutput(raw, err, time.Since(start))
}

func parseCurlMirrorProbeOutput(raw string, runErr error, wallElapsed time.Duration) (bool, int64, string) {
	idx := strings.LastIndex(raw, "__DK_PROBE__:")
	var headersPart string
	var statusCode int
	var latencyMs int64
	if idx >= 0 {
		headersPart = raw[:idx]
		meta := strings.TrimSpace(raw[idx+len("__DK_PROBE__:"):])
		if nl := strings.IndexByte(meta, '\n'); nl >= 0 {
			meta = strings.TrimSpace(meta[:nl])
		}
		codeStr, timeStr, ok := strings.Cut(meta, ":")
		if ok {
			statusCode, _ = strconv.Atoi(strings.TrimSpace(codeStr))
			if sec, fErr := strconv.ParseFloat(strings.TrimSpace(timeStr), 64); fErr == nil && sec > 0 {
				latencyMs = int64(sec * 1000)
			}
		}
	} else {
		headersPart = raw
	}
	if latencyMs <= 0 {
		latencyMs = wallElapsed.Milliseconds()
	}

	if statusCode == http.StatusUnauthorized || statusCode == http.StatusOK {
		lowerHdr := strings.ToLower(headersPart)
		// 拦截已关停镜像源返回 HTML 公告页（200 text/html 且无 Docker V2 响应头）的假阳性
		hasRegistryHeader := strings.Contains(lowerHdr, "docker-distribution-api-version") ||
			strings.Contains(lowerHdr, "www-authenticate")
		if statusCode == http.StatusOK && !hasRegistryHeader && strings.Contains(lowerHdr, "text/html") {
			return false, latencyMs, "not a valid Docker registry endpoint"
		}
		if latencyMs < 1 {
			latencyMs = 1
		}
		return true, latencyMs, ""
	}

	if statusCode > 0 {
		return false, latencyMs, fmt.Sprintf("HTTP %d", statusCode)
	}
	errLine := strings.TrimSpace(headersPart)
	if errLine == "" && runErr != nil {
		errLine = runErr.Error()
	}
	if nl := strings.IndexByte(errLine, '\n'); nl >= 0 {
		errLine = strings.TrimSpace(errLine[:nl])
	}
	if errLine == "" {
		errLine = "connection failed"
	}
	return false, 0, errLine
}

// DefaultMirrorHTTPClient 返回带默认超时与代理环境的 HTTP 客户端。
func DefaultMirrorHTTPClient() (*http.Client, func()) {
	tr := &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	hc := &http.Client{
		Timeout:   8 * time.Second,
		Transport: tr,
	}
	return hc, tr.CloseIdleConnections
}

// NewHostHTTPClient 在本机传输模式下直接返回本机网络与代理环境的 HTTP 客户端。
func (l *LocalTransport) NewHostHTTPClient(_ context.Context) (*http.Client, func()) {
	return DefaultMirrorHTTPClient()
}

// NewHostHTTPClient 在远端 SSH 主机模式下构造经由远端主机发起 TCP 连接（并继承远端 docker.service 代理配置）的 HTTP 客户端。
func (t *SSHTransport) NewHostHTTPClient(ctx context.Context) (*http.Client, func()) {
	if err := t.ensureClient(ctx); err != nil {
		return DefaultMirrorHTTPClient()
	}
	cli, err := t.getClient()
	if err != nil {
		return DefaultMirrorHTTPClient()
	}

	var httpProxy, httpsProxy, noProxy string
	eCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	out, eErr := t.runSession(
		eCtx,
		`sh -c 'if command -v systemctl >/dev/null 2>&1; then systemctl show -p Environment docker 2>/dev/null; fi'`,
		nil,
		16<<10,
		true,
	)
	cancel()
	if eErr == nil && len(out) > 0 {
		httpProxy, httpsProxy, noProxy = parseDockerSystemdProxyEnv(string(out))
	}

	localDialer := &net.Dialer{Timeout: 6 * time.Second}
	tr := &http.Transport{
		Proxy:           buildCustomProxyFunc(httpProxy, httpsProxy, noProxy),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(dctx context.Context, network, addr string) (net.Conn, error) {
			conn, dErr := cli.DialContext(dctx, network, addr)
			if dErr != nil && strings.Contains(strings.ToLower(dErr.Error()), "prohibited") {
				return localDialer.DialContext(dctx, network, addr)
			}
			return conn, dErr
		},
	}
	hc := &http.Client{
		Timeout:   9 * time.Second,
		Transport: tr,
	}
	return hc, tr.CloseIdleConnections
}

func parseDockerSystemdProxyEnv(raw string) (httpProxy, httpsProxy, noProxy string) {
	line := strings.TrimSpace(raw)
	line = strings.TrimPrefix(line, "Environment=")
	for _, token := range splitSystemdEnvTokens(line) {
		k, v, ok := strings.Cut(token, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.ToUpper(strings.TrimSpace(k)) {
		case "HTTP_PROXY":
			if httpProxy == "" {
				httpProxy = v
			}
		case "HTTPS_PROXY":
			if httpsProxy == "" {
				httpsProxy = v
			}
		case "NO_PROXY":
			if noProxy == "" {
				noProxy = v
			}
		}
	}
	return httpProxy, httpsProxy, noProxy
}

func splitSystemdEnvTokens(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inQuote != 0 {
			if ch == inQuote {
				inQuote = 0
			} else {
				cur.WriteByte(ch)
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inQuote = ch
			continue
		}
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteByte(ch)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func buildCustomProxyFunc(httpProxy, httpsProxy, noProxy string) func(*http.Request) (*url.URL, error) {
	if httpProxy == "" && httpsProxy == "" {
		return nil
	}
	var bypass []string
	for _, p := range strings.Split(noProxy, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			bypass = append(bypass, p)
		}
	}
	parseP := func(raw string) *url.URL {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil
		}
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return nil
		}
		return u
	}
	httpU := parseP(httpProxy)
	httpsU := parseP(httpsProxy)
	if httpsU == nil {
		httpsU = httpU
	}
	return func(req *http.Request) (*url.URL, error) {
		if req == nil || req.URL == nil {
			return nil, nil
		}
		host := strings.ToLower(req.URL.Hostname())
		for _, bp := range bypass {
			if bp == "*" || host == bp || strings.HasSuffix(host, "."+strings.TrimPrefix(bp, ".")) {
				return nil, nil
			}
		}
		if strings.EqualFold(req.URL.Scheme, "https") {
			return httpsU, nil
		}
		return httpU, nil
	}
}

const ociManifestAcceptHeader = "application/vnd.oci.image.index.v1+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.docker.distribution.manifest.v2+json, " +
	"application/vnd.docker.distribution.manifest.v1+prettyjws"

// ProbeMirrorPullWithClient 通过给定的目标设备 HTTP 客户端对单个加速源执行完整的连通、鉴权换票、镜像清单与镜像层首字节验证。
func ProbeMirrorPullWithClient(
	ctx context.Context,
	hc *http.Client,
	mirrorURL string,
	source string,
	cand platform.MirrorProbeCandidate,
	hasSpecificApp bool,
) platform.MirrorTestItem {
	clean := strings.TrimRight(strings.TrimSpace(mirrorURL), "/")
	if clean == "" {
		return platform.MirrorTestItem{URL: mirrorURL, OK: false, Status: "dead", Error: "invalid url"}
	}
	if hc == nil {
		var cleanup func()
		hc, cleanup = DefaultMirrorHTTPClient()
		defer cleanup()
	}

	pctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	// 1. 验证 /v2/ 仓库端点连通性与响应头合法性（拦截已关停源的 HTML 公告页）
	t0 := time.Now()
	v2Req, err := http.NewRequestWithContext(pctx, http.MethodGet, clean+"/v2/", nil)
	if err != nil {
		return platform.MirrorTestItem{URL: clean, OK: false, Status: "dead", Error: err.Error()}
	}
	v2Resp, err := hc.Do(v2Req)
	v2Ms := time.Since(t0).Milliseconds()
	if v2Ms < 1 {
		v2Ms = 1
	}
	if err != nil {
		return platform.MirrorTestItem{URL: clean, OK: false, Status: "dead", Error: err.Error()}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(v2Resp.Body, 4096))
	_ = v2Resp.Body.Close()

	if v2Resp.StatusCode != http.StatusOK && v2Resp.StatusCode != http.StatusUnauthorized {
		return platform.MirrorTestItem{
			URL:        clean,
			OK:         false,
			Status:     "dead",
			HTTPStatus: v2Resp.StatusCode,
			Error:      fmt.Sprintf("HTTP %d", v2Resp.StatusCode),
		}
	}
	ct := strings.ToLower(v2Resp.Header.Get("Content-Type"))
	hasRegHdr := v2Resp.Header.Get("Docker-Distribution-Api-Version") != "" ||
		v2Resp.Header.Get("Www-Authenticate") != ""
	if v2Resp.StatusCode == http.StatusOK && !hasRegHdr && strings.Contains(ct, "text/html") {
		return platform.MirrorTestItem{
			URL:    clean,
			OK:     false,
			Status: "dead",
			Error:  "not a valid Docker registry endpoint",
		}
	}

	// 若无具体镜像可测（如非 docker.io 仓库且本机暂无容器、用户也未指定镜像），如实返回基础连通状态
	repoPath := strings.Trim(strings.TrimSpace(cand.RepoPath), "/")
	tag := strings.TrimSpace(cand.Tag)
	if tag == "" {
		tag = "latest"
	}
	if repoPath == "" {
		return platform.MirrorTestItem{
			URL:       clean,
			OK:        true,
			Status:    "basic_ok",
			LatencyMs: v2Ms,
		}
	}

	testedImg := repoPath + ":" + tag
	nsSuffix := ""
	if strings.EqualFold(strings.TrimSpace(source), "docker.io") {
		nsSuffix = "?ns=docker.io"
	}
	manifestURL := fmt.Sprintf("%s/v2/%s/manifests/%s%s", clean, repoPath, url.PathEscape(tag), nsSuffix)

	// 2. 请求目标镜像清单（若 401 Bearer 则按挑战头自动换取匿名拉取令牌）
	bearerToken := ""
	mResp, mBody, mErr := doRegistryGET(pctx, hc, manifestURL, ociManifestAcceptHeader, "", 512<<10)
	if mErr != nil {
		return platform.MirrorTestItem{
			URL:         clean,
			OK:          false,
			Status:      "dead",
			TestedImage: testedImg,
			Error:       mErr.Error(),
		}
	}
	if mResp.StatusCode == http.StatusUnauthorized {
		wwwAuth := mResp.Header.Get("Www-Authenticate")
		if wwwAuth == "" {
			wwwAuth = v2Resp.Header.Get("Www-Authenticate")
		}
		tok, tStatus, tErr := fetchAnonymousBearerToken(pctx, hc, wwwAuth, repoPath)
		if tErr != nil {
			st := "dead"
			if tStatus == http.StatusUnauthorized || tStatus == http.StatusForbidden || tStatus == http.StatusTooManyRequests {
				st = "restricted"
			}
			return platform.MirrorTestItem{
				URL:         clean,
				OK:          false,
				Status:      st,
				HTTPStatus:  tStatus,
				TestedImage: testedImg,
				Error:       tErr.Error(),
			}
		}
		bearerToken = tok
		mResp, mBody, mErr = doRegistryGET(pctx, hc, manifestURL, ociManifestAcceptHeader, bearerToken, 512<<10)
		if mErr != nil {
			return platform.MirrorTestItem{
				URL:         clean,
				OK:          false,
				Status:      "dead",
				TestedImage: testedImg,
				Error:       mErr.Error(),
			}
		}
	}

	switch mResp.StatusCode {
	case http.StatusOK:
		// 继续解析清单并验证镜像层首字节
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusUnavailableForLegalReasons:
		return platform.MirrorTestItem{
			URL:         clean,
			OK:          false,
			Status:      "restricted",
			HTTPStatus:  mResp.StatusCode,
			TestedImage: testedImg,
			Error:       fmt.Sprintf("HTTP %d", mResp.StatusCode),
		}
	case http.StatusNotFound:
		return platform.MirrorTestItem{
			URL:         clean,
			OK:          false,
			Status:      "not_found",
			HTTPStatus:  http.StatusNotFound,
			TestedImage: testedImg,
			Error:       "HTTP 404",
		}
	default:
		return platform.MirrorTestItem{
			URL:         clean,
			OK:          false,
			Status:      "dead",
			HTTPStatus:  mResp.StatusCode,
			TestedImage: testedImg,
			Error:       fmt.Sprintf("HTTP %d", mResp.StatusCode),
		}
	}

	layerDigest, subManifestDigest, validMf := extractProbeManifestDigests(mBody)
	if !validMf {
		return platform.MirrorTestItem{
			URL:         clean,
			OK:          false,
			Status:      "dead",
			TestedImage: testedImg,
			Error:       "invalid manifest payload",
		}
	}
	if layerDigest == "" && subManifestDigest != "" {
		subURL := fmt.Sprintf("%s/v2/%s/manifests/%s%s", clean, repoPath, url.PathEscape(subManifestDigest), nsSuffix)
		subResp, subBody, subErr := doRegistryGET(pctx, hc, subURL, ociManifestAcceptHeader, bearerToken, 512<<10)
		if subErr == nil && subResp.StatusCode == http.StatusOK {
			layerDigest, _, _ = extractProbeManifestDigests(subBody)
		}
	}

	// 3. 试读镜像层首字节（Range: bytes=0-0，仅 1 字节流量，验证层存储与 CDN 重定向是否畅通）
	if layerDigest != "" {
		blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s%s", clean, repoPath, url.PathEscape(layerDigest), nsSuffix)
		bReq, bErr := http.NewRequestWithContext(pctx, http.MethodGet, blobURL, nil)
		if bErr == nil {
			bReq.Header.Set("Range", "bytes=0-0")
			if bearerToken != "" {
				bReq.Header.Set("Authorization", "Bearer "+bearerToken)
			}
			bResp, dErr := hc.Do(bReq)
			if dErr != nil {
				return platform.MirrorTestItem{
					URL:         clean,
					OK:          false,
					Status:      "dead",
					TestedImage: testedImg,
					Error:       dErr.Error(),
				}
			}
			var one [1]byte
			_, _ = io.ReadFull(bResp.Body, one[:])
			_ = bResp.Body.Close()

			switch bResp.StatusCode {
			case http.StatusOK, http.StatusPartialContent:
				// 首字节验证通过
			case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusUnavailableForLegalReasons:
				return platform.MirrorTestItem{
					URL:         clean,
					OK:          false,
					Status:      "restricted",
					HTTPStatus:  bResp.StatusCode,
					TestedImage: testedImg,
					Error:       fmt.Sprintf("HTTP %d", bResp.StatusCode),
				}
			case http.StatusNotFound:
				return platform.MirrorTestItem{
					URL:         clean,
					OK:          false,
					Status:      "not_found",
					HTTPStatus:  http.StatusNotFound,
					TestedImage: testedImg,
					Error:       "HTTP 404",
				}
			default:
				return platform.MirrorTestItem{
					URL:         clean,
					OK:          false,
					Status:      "dead",
					HTTPStatus:  bResp.StatusCode,
					TestedImage: testedImg,
					Error:       fmt.Sprintf("HTTP %d", bResp.StatusCode),
				}
			}
		}
	}

	finalStatus := "ok"
	if !hasSpecificApp {
		finalStatus = "basic_ok"
	}
	return platform.MirrorTestItem{
		URL:         clean,
		OK:          true,
		Status:      finalStatus,
		LatencyMs:   v2Ms,
		TestedImage: testedImg,
	}
}

func doRegistryGET(ctx context.Context, hc *http.Client, rawURL, acceptHdr, bearerToken string, maxBytes int64) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	if acceptHdr != "" {
		req.Header.Set("Accept", acceptHdr)
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	return resp, body, nil
}

func fetchAnonymousBearerToken(ctx context.Context, hc *http.Client, wwwAuth, repoPath string) (string, int, error) {
	trimmed := strings.TrimSpace(wwwAuth)
	if len(trimmed) < 7 || !strings.EqualFold(trimmed[:7], "bearer ") {
		return "", http.StatusUnauthorized, fmt.Errorf("HTTP 401")
	}
	params := parseAuthChallengeParams(trimmed[7:])
	realm := strings.TrimSpace(params["realm"])
	if realm == "" {
		return "", http.StatusUnauthorized, fmt.Errorf("HTTP 401")
	}
	u, err := url.Parse(realm)
	if err != nil {
		return "", 0, err
	}
	q := u.Query()
	if svc := strings.TrimSpace(params["service"]); svc != "" {
		q.Set("service", svc)
	}
	scope := strings.TrimSpace(params["scope"])
	if scope == "" {
		scope = "repository:" + repoPath + ":pull"
	}
	q.Set("scope", scope)
	u.RawQuery = q.Encode()

	resp, body, err := doRegistryGET(ctx, hc, u.String(), "application/json", "", 64<<10)
	if err != nil {
		return "", 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var tokObj struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tokObj); err != nil {
		return "", resp.StatusCode, fmt.Errorf("invalid token response")
	}
	tok := strings.TrimSpace(tokObj.Token)
	if tok == "" {
		tok = strings.TrimSpace(tokObj.AccessToken)
	}
	if tok == "" {
		return "", http.StatusUnauthorized, fmt.Errorf("empty token")
	}
	return tok, http.StatusOK, nil
}

func parseAuthChallengeParams(s string) map[string]string {
	out := make(map[string]string)
	i := 0
	n := len(s)
	for i < n {
		for i < n && (s[i] == ' ' || s[i] == ',' || s[i] == '\t') {
			i++
		}
		startKey := i
		for i < n && s[i] != '=' && s[i] != ',' && s[i] != ' ' {
			i++
		}
		key := strings.ToLower(strings.TrimSpace(s[startKey:i]))
		for i < n && s[i] == ' ' {
			i++
		}
		if i >= n || s[i] != '=' {
			for i < n && s[i] != ',' {
				i++
			}
			continue
		}
		i++ // skip '='
		for i < n && s[i] == ' ' {
			i++
		}
		var val strings.Builder
		if i < n && s[i] == '"' {
			i++
			for i < n && s[i] != '"' {
				if s[i] == '\\' && i+1 < n {
					i++
				}
				val.WriteByte(s[i])
				i++
			}
			if i < n && s[i] == '"' {
				i++
			}
		} else {
			startVal := i
			for i < n && s[i] != ',' && s[i] != ' ' {
				i++
			}
			val.WriteString(s[startVal:i])
		}
		if key != "" {
			out[key] = val.String()
		}
	}
	return out
}

func extractProbeManifestDigests(body []byte) (layerDigest, subManifestDigest string, valid bool) {
	var mf struct {
		SchemaVersion int `json:"schemaVersion"`
		Layers        []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
		FSLayers []struct {
			BlobSum string `json:"blobSum"`
		} `json:"fsLayers"`
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS   string `json:"os"`
				Arch string `json:"arch"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(body, &mf); err != nil {
		return "", "", false
	}
	if mf.SchemaVersion == 0 && len(mf.Layers) == 0 && len(mf.FSLayers) == 0 && len(mf.Manifests) == 0 {
		return "", "", false
	}
	for _, l := range mf.Layers {
		if strings.TrimSpace(l.Digest) != "" {
			return strings.TrimSpace(l.Digest), "", true
		}
	}
	for _, fl := range mf.FSLayers {
		if strings.TrimSpace(fl.BlobSum) != "" {
			return strings.TrimSpace(fl.BlobSum), "", true
		}
	}
	if len(mf.Manifests) > 0 {
		chosen := strings.TrimSpace(mf.Manifests[0].Digest)
		for _, m := range mf.Manifests {
			if m.Platform.OS == "linux" && m.Platform.Arch == "amd64" && strings.TrimSpace(m.Digest) != "" {
				chosen = strings.TrimSpace(m.Digest)
				break
			}
		}
		return "", chosen, true
	}
	return "", "", true
}

// ProbeMirrorViaGoHTTP 使用当前进程 HTTP 客户端探测 <mirrorURL>/v2/ 是否为有效 OCI Registry 端点。
func ProbeMirrorViaGoHTTP(ctx context.Context, mirrorURL string) (bool, int64, string) {
	clean := strings.TrimRight(strings.TrimSpace(mirrorURL), "/")
	if clean == "" {
		return false, 0, "invalid url"
	}
	pctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	hc := &http.Client{
		Timeout: 7 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	_ = os.Getenv("HTTPS_PROXY")
	start := time.Now()
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, clean+"/v2/", nil)
	if err != nil {
		return false, 0, err.Error()
	}
	resp, err := hc.Do(req)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return false, 0, err.Error()
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusOK {
		ct := strings.ToLower(resp.Header.Get("Content-Type"))
		hasRegHdr := resp.Header.Get("Docker-Distribution-Api-Version") != "" ||
			resp.Header.Get("Www-Authenticate") != ""
		if resp.StatusCode == http.StatusOK && !hasRegHdr && strings.Contains(ct, "text/html") {
			return false, elapsed, "not a valid Docker registry endpoint"
		}
		if elapsed < 1 {
			elapsed = 1
		}
		return true, elapsed, ""
	}
	return false, elapsed, fmt.Sprintf("HTTP %d", resp.StatusCode)
}

package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// MirrorConfigTransport 扩展 ExecutableTransport，提供宿主机 Docker 配置热重载与从目标设备发起的镜像源测速能力。
type MirrorConfigTransport interface {
	ExecutableTransport
	ReloadDockerDaemon(ctx context.Context) error
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

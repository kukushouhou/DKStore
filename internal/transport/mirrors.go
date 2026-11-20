package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
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

// HostDaemonConfigState 描述目标宿主机当前实际运行的 dockerd 配置文件路径及内容（兼容 --config-file 与 OpenWrt UCI）。
type HostDaemonConfigState struct {
	ActiveConfigPath  string
	ConfigBytes       []byte
	Exists            bool
	DefaultJSONBytes  []byte
	DefaultJSONExists bool
	HasOpenWrtUCI     bool
	OpenWrtUCIBytes   []byte
}

// ApplyDaemonMirrorPlan 描述一次对宿主机 Docker 镜像源配置的原子写入与热重载计划。
type ApplyDaemonMirrorPlan struct {
	WriteActiveConfig   bool
	ActiveConfigPath    string
	ActiveConfigBytes   []byte
	WriteDefaultJSON    bool
	DefaultJSONBytes    []byte
	WriteOpenWrtUCI     bool
	OpenWrtUCIBytes     []byte
	ReloadDaemon        bool
	ManageCertsDir      bool
	CustomCertHostsTOML map[string][]byte
}

// DaemonConfigTransport 扩展ExecutableTransport，支持探测 dockerd 实际配置文件（含 OpenWrt UCI 与容器部署穿透宿主机）并原子应用。
type DaemonConfigTransport interface {
	InspectDockerDaemonConfig(ctx context.Context) (*HostDaemonConfigState, error)
	ApplyDockerDaemonConfig(ctx context.Context, plan ApplyDaemonMirrorPlan) error
}

const findDockerdPidsFunc = `
find_dockerd_pids() {
  found=""
  for d in /proc/[0-9]*; do
    [ -r "$d/comm" ] || continue
    cname=$(cat "$d/comm" 2>/dev/null || true)
    if [ "$cname" = "dockerd" ]; then
      p=${d#/proc/}
      found="$found $p"
    fi
  done
  if [ -z "$found" ] && command -v pidof >/dev/null 2>&1; then
    found=$(pidof dockerd 2>/dev/null || true)
  fi
  if [ -z "$found" ] && [ -f "${HOST_ROOT:-}/var/run/docker.pid" ]; then
    found=$(cat "${HOST_ROOT:-}/var/run/docker.pid" 2>/dev/null || true)
  fi
  printf '%s' "$found"
}
`

func buildReloadDockerScript(hostRoot string) string {
	return fmt.Sprintf(`LC_ALL=C; PATH="$PATH:/usr/local/bin:/usr/sbin:/sbin:/bin:/usr/bin"
HOST_ROOT=%s
if [ "$(id -u)" != "0" ]; then
  echo "permission denied: root required to reload dockerd" >&2
  exit 13
fi
%s
if [ -z "$HOST_ROOT" ] && command -v systemctl >/dev/null 2>&1 && systemctl reload docker >/dev/null 2>&1; then
  exit 0
fi
pids=$(find_dockerd_pids)
if [ -n "$pids" ]; then
  kill -HUP $pids && exit 0
fi
if [ -z "$HOST_ROOT" ] && command -v pkill >/dev/null 2>&1 && pkill -HUP -x dockerd >/dev/null 2>&1; then
  exit 0
fi
echo "dockerd process not found for reload" >&2
exit 1`, shellQuote(hostRoot), findDockerdPidsFunc)
}

const (
	markerInspectMeta   = "__DKSTORE_CFG_META__"
	markerActiveBegin   = "__DKSTORE_ACTIVE_BEGIN__"
	markerActiveEnd     = "__DKSTORE_ACTIVE_END__"
	markerDefaultBegin  = "__DKSTORE_DEFAULT_BEGIN__"
	markerDefaultEnd    = "__DKSTORE_DEFAULT_END__"
	markerOpenWrtBegin  = "__DKSTORE_OPENWRT_UCI_BEGIN__"
	markerOpenWrtEnd    = "__DKSTORE_OPENWRT_UCI_END__"
	heredocApplyContent = "__DKSTORE_EOF_9F8A7B6C__"
)

func buildInspectDaemonConfigScript(hostRoot string) string {
	return fmt.Sprintf(`LC_ALL=C; PATH="$PATH:/usr/local/bin:/usr/sbin:/sbin:/bin:/usr/bin"
HOST_ROOT=%s
%s
pids=$(find_dockerd_pids)
first_pid=""
for p in $pids; do first_pid="$p"; break; done
cfg=""
if [ -n "$first_pid" ] && [ -r "/proc/$first_pid/cmdline" ]; then
  prev=""
  while IFS= read -r arg; do
    if [ "$prev" = "--config-file" ]; then
      cfg="$arg"
      break
    fi
    case "$arg" in
      --config-file=*)
        cfg=${arg#--config-file=}
        break
        ;;
      --config-file)
        prev="--config-file"
        ;;
    esac
  done <<__DK_CMDLINE__
$(tr '\000' '\n' < "/proc/$first_pid/cmdline" 2>/dev/null)
__DK_CMDLINE__
fi
case "$cfg" in
  /*) active_path="$cfg" ;;
  *)  active_path="/etc/docker/daemon.json" ;;
esac

real_active="${HOST_ROOT}${active_path}"
active_exists=0
if [ -e "$real_active" ]; then
  if [ ! -r "$real_active" ]; then
    echo "permission denied: $active_path" >&2
    exit 13
  fi
  active_exists=1
fi

real_def="${HOST_ROOT}/etc/docker/daemon.json"
def_exists=0
if [ "$active_path" != "/etc/docker/daemon.json" ] && [ -e "$real_def" ]; then
  if [ ! -r "$real_def" ]; then
    echo "permission denied: /etc/docker/daemon.json" >&2
    exit 13
  fi
  def_exists=1
fi

real_uci="${HOST_ROOT}/etc/config/dockerd"
uci_exists=0
if [ -f "$real_uci" ]; then
  if [ ! -r "$real_uci" ]; then
    echo "permission denied: /etc/config/dockerd" >&2
    exit 13
  fi
  uci_exists=1
fi

printf '%s\t%%s\t%%d\t%%d\t%%d\n' "$active_path" "$active_exists" "$def_exists" "$uci_exists"
if [ "$active_exists" = "1" ]; then
  printf '%s\n'
  cat -- "$real_active"
  printf '\n%s\n'
fi
if [ "$def_exists" = "1" ]; then
  printf '%s\n'
  cat -- "$real_def"
  printf '\n%s\n'
fi
if [ "$uci_exists" = "1" ]; then
  printf '%s\n'
  cat -- "$real_uci"
  printf '\n%s\n'
fi
`, shellQuote(hostRoot), findDockerdPidsFunc, markerInspectMeta,
		markerActiveBegin, markerActiveEnd,
		markerDefaultBegin, markerDefaultEnd,
		markerOpenWrtBegin, markerOpenWrtEnd)
}

func extractMarkedBlock(s, beginMarker, endMarker string) []byte {
	bIdx := strings.Index(s, beginMarker+"\n")
	if bIdx < 0 {
		return nil
	}
	rest := s[bIdx+len(beginMarker)+1:]
	eIdx := strings.Index(rest, "\n"+endMarker)
	if eIdx < 0 {
		return nil
	}
	return []byte(rest[:eIdx])
}

func parseInspectDaemonConfigOutput(raw string) (*HostDaemonConfigState, error) {
	norm := strings.ReplaceAll(raw, "\r\n", "\n")
	st := &HostDaemonConfigState{ActiveConfigPath: "/etc/docker/daemon.json"}
	gotMeta := false
	for _, line := range strings.Split(norm, "\n") {
		if strings.HasPrefix(line, markerInspectMeta+"\t") {
			parts := strings.Split(line, "\t")
			if len(parts) >= 5 {
				gotMeta = true
				if p := strings.TrimSpace(parts[1]); strings.HasPrefix(p, "/") {
					st.ActiveConfigPath = p
				}
				st.Exists = parts[2] == "1"
				st.DefaultJSONExists = parts[3] == "1"
				st.HasOpenWrtUCI = parts[4] == "1"
			}
			break
		}
	}
	if !gotMeta {
		return nil, fmt.Errorf("unexpected daemon config inspect output")
	}
	if st.Exists {
		st.ConfigBytes = extractMarkedBlock(norm, markerActiveBegin, markerActiveEnd)
	}
	if st.DefaultJSONExists {
		st.DefaultJSONBytes = extractMarkedBlock(norm, markerDefaultBegin, markerDefaultEnd)
	}
	if st.HasOpenWrtUCI {
		st.OpenWrtUCIBytes = extractMarkedBlock(norm, markerOpenWrtBegin, markerOpenWrtEnd)
	}
	return st, nil
}

func appendAtomicHeredocWrite(sb *strings.Builder, hostRoot, posixPath string, content []byte) {
	fullPath := hostRoot + posixPath
	tmpPath := fullPath + ".dkstore.tmp"
	dir := posixParentDir(fullPath)
	if dir != "" && dir != "." && dir != "/" {
		sb.WriteString("mkdir -p -- " + shellQuote(dir) + "\n")
	}
	sb.WriteString(fmt.Sprintf("{ cp -p -- %s %s 2>/dev/null || :; }\n", shellQuote(fullPath), shellQuote(tmpPath)))
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	text = strings.ReplaceAll(text, heredocApplyContent, "")
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	sb.WriteString(fmt.Sprintf("cat <<'%s' > %s\n%s%s\n", heredocApplyContent, shellQuote(tmpPath), text, heredocApplyContent))
	sb.WriteString(fmt.Sprintf("mv -f -- %s %s\n", shellQuote(tmpPath), shellQuote(fullPath)))
}

func buildApplyDaemonConfigScript(hostRoot string, plan ApplyDaemonMirrorPlan) string {
	var sb strings.Builder
	sb.WriteString("set -e\nLC_ALL=C; PATH=\"$PATH:/usr/local/bin:/usr/sbin:/sbin:/bin:/usr/bin\"\n")
	sb.WriteString("HOST_ROOT=" + shellQuote(hostRoot) + "\n")
	sb.WriteString("if [ \"$(id -u)\" != \"0\" ]; then\n  echo \"permission denied: root required\" >&2\n  exit 13\nfi\n")
	sb.WriteString(findDockerdPidsFunc + "\n")

	if plan.WriteActiveConfig && strings.HasPrefix(plan.ActiveConfigPath, "/") {
		appendAtomicHeredocWrite(&sb, hostRoot, plan.ActiveConfigPath, plan.ActiveConfigBytes)
	}
	if plan.WriteDefaultJSON && plan.ActiveConfigPath != "/etc/docker/daemon.json" {
		appendAtomicHeredocWrite(&sb, hostRoot, "/etc/docker/daemon.json", plan.DefaultJSONBytes)
	}
	if plan.WriteOpenWrtUCI {
		appendAtomicHeredocWrite(&sb, hostRoot, "/etc/config/dockerd", plan.OpenWrtUCIBytes)
	}

	if plan.ManageCertsDir {
		var srcs []string
		for s := range plan.CustomCertHostsTOML {
			srcs = append(srcs, s)
		}
		sort.Strings(srcs)
		for _, src := range srcs {
			tomlBytes := plan.CustomCertHostsTOML[src]
			if len(tomlBytes) > 0 {
				appendAtomicHeredocWrite(&sb, hostRoot, "/etc/docker/certs.d/"+src+"/hosts.toml", tomlBytes)
			}
		}
		certsBase := hostRoot + "/etc/docker/certs.d"
		sb.WriteString(fmt.Sprintf("if [ -d %s ]; then\n", shellQuote(certsBase)))
		sb.WriteString(fmt.Sprintf("  for d in %s/*; do\n", shellQuote(certsBase)))
		sb.WriteString("    [ -d \"$d\" ] || continue\n")
		sb.WriteString("    name=${d##*/}\n")
		sb.WriteString("    keep=0\n")
		if len(srcs) > 0 {
			quotedKeep := make([]string, 0, len(srcs))
			for _, s := range srcs {
				quotedKeep = append(quotedKeep, shellQuote(s))
			}
			sb.WriteString(fmt.Sprintf("    for k in %s; do [ \"$name\" = \"$k\" ] && keep=1; done\n", strings.Join(quotedKeep, " ")))
		}
		sb.WriteString("    if [ \"$keep\" = \"0\" ] && [ -f \"$d/hosts.toml\" ]; then\n")
		sb.WriteString("      first_line=$(head -n 1 \"$d/hosts.toml\" 2>/dev/null || true)\n")
		sb.WriteString("      if [ \"$first_line\" = \"# Managed by DKStore\" ]; then\n")
		sb.WriteString("        rm -f -- \"$d/hosts.toml\"\n")
		sb.WriteString("        rmdir -- \"$d\" 2>/dev/null || true\n")
		sb.WriteString("      fi\n")
		sb.WriteString("    fi\n")
		sb.WriteString("  done\nfi\n")
	}

	if plan.ReloadDaemon {
		sb.WriteString("reloaded=0\n")
		sb.WriteString("if [ -z \"$HOST_ROOT\" ] && command -v systemctl >/dev/null 2>&1 && systemctl reload docker >/dev/null 2>&1; then reloaded=1; fi\n")
		sb.WriteString("if [ \"$reloaded\" = \"0\" ]; then\n")
		sb.WriteString("  pids=$(find_dockerd_pids)\n")
		sb.WriteString("  if [ -n \"$pids\" ]; then kill -HUP $pids && reloaded=1; fi\n")
		sb.WriteString("fi\n")
		sb.WriteString("if [ \"$reloaded\" = \"0\" ] && [ -z \"$HOST_ROOT\" ] && command -v pkill >/dev/null 2>&1 && pkill -HUP -x dockerd >/dev/null 2>&1; then reloaded=1; fi\n")
		sb.WriteString("if [ \"$reloaded\" = \"0\" ]; then echo \"dockerd process not found for reload\" >&2; exit 1; fi\n")
	}
	return sb.String()
}

var containerIDHexRe = regexp.MustCompile(`(?i)(?:containers/|docker-)([0-9a-f]{64})`)

func isRunningInsideContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	return false
}

func (l *LocalTransport) resolveSelfHelperImage(ctx context.Context) (string, error) {
	tryInspect := func(idOrName string) string {
		idOrName = strings.TrimSpace(idOrName)
		if idOrName == "" {
			return ""
		}
		resp, err := l.RequestDocker(ctx, http.MethodGet, "/containers/"+url.PathEscape(idOrName)+"/json", nil, nil)
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return ""
		}
		var info struct {
			Image string `json:"Image"`
		}
		if json.NewDecoder(resp.Body).Decode(&info) == nil && strings.TrimSpace(info.Image) != "" {
			return strings.TrimSpace(info.Image)
		}
		return ""
	}

	for _, procFile := range []string{"/proc/self/mountinfo", "/proc/self/cgroup"} {
		if b, err := os.ReadFile(procFile); err == nil {
			if m := containerIDHexRe.FindSubmatch(b); len(m) == 2 {
				if img := tryInspect(string(m[1])); img != "" {
					return img, nil
				}
			}
		}
	}
	if hn, err := os.Hostname(); err == nil {
		if img := tryInspect(hn); img != "" {
			return img, nil
		}
	}
	resp, err := l.RequestDocker(ctx, http.MethodGet, "/containers/json?all=1", nil, nil)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var list []struct {
				ImageID string            `json:"ImageID"`
				Image   string            `json:"Image"`
				Command string            `json:"Command"`
				Labels  map[string]string `json:"Labels"`
			}
			if json.NewDecoder(resp.Body).Decode(&list) == nil {
				for _, c := range list {
					if c.Labels["dkstore.self"] == "true" && strings.TrimSpace(c.ImageID) != "" {
						return strings.TrimSpace(c.ImageID), nil
					}
				}
				for _, c := range list {
					if strings.Contains(strings.ToLower(c.Command), "dkstore") && strings.TrimSpace(c.ImageID) != "" {
						return strings.TrimSpace(c.ImageID), nil
					}
				}
			}
		}
	}
	return "", fmt.Errorf("cannot locate local helper container image")
}

func demuxDockerLogs(r io.Reader) (stdout, stderr []byte) {
	var outBuf, errBuf bytes.Buffer
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if outBuf.Len() == 0 && errBuf.Len() == 0 {
				rest, _ := io.ReadAll(io.LimitReader(r, 256<<10))
				outBuf.Write(rest)
			}
			break
		}
		sz := binary.BigEndian.Uint32(hdr[4:8])
		if sz == 0 {
			continue
		}
		if sz > 4<<20 {
			break
		}
		chunk := make([]byte, sz)
		if _, err := io.ReadFull(r, chunk); err != nil {
			break
		}
		if hdr[0] == 2 {
			errBuf.Write(chunk)
		} else {
			outBuf.Write(chunk)
		}
	}
	return outBuf.Bytes(), errBuf.Bytes()
}

func (l *LocalTransport) runContainerHostScript(ctx context.Context, script string) ([]byte, error) {
	img, err := l.resolveSelfHelperImage(ctx)
	if err != nil {
		return nil, err
	}
	createSpec := map[string]any{
		"Image":      img,
		"User":       "0:0",
		"Entrypoint": []string{"/bin/sh", "-c"},
		"Cmd":        []string{script},
		"HostConfig": map[string]any{
			"PidMode":     "host",
			"NetworkMode": "none",
			"Binds":       []string{"/:/host_root"},
		},
	}
	bodyBytes, err := json.Marshal(createSpec)
	if err != nil {
		return nil, err
	}
	cResp, err := l.RequestDocker(ctx, http.MethodPost, "/containers/create", bytes.NewReader(bodyBytes), map[string]string{
		"Content-Type": "application/json",
	})
	if err != nil {
		return nil, err
	}
	cBody, _ := io.ReadAll(io.LimitReader(cResp.Body, 16<<10))
	_ = cResp.Body.Close()
	if cResp.StatusCode < 200 || cResp.StatusCode >= 300 {
		return nil, fmt.Errorf("create host config helper failed (HTTP %d): %s", cResp.StatusCode, strings.TrimSpace(string(cBody)))
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(cBody, &created); err != nil || strings.TrimSpace(created.ID) == "" {
		return nil, fmt.Errorf("invalid helper container response")
	}
	cid := strings.TrimSpace(created.ID)
	defer func() {
		dCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if dResp, dErr := l.RequestDocker(dCtx, http.MethodDelete, "/containers/"+url.PathEscape(cid)+"?force=1&v=1", nil, nil); dErr == nil && dResp != nil {
			_ = dResp.Body.Close()
		}
	}()

	sResp, err := l.RequestDocker(ctx, http.MethodPost, "/containers/"+url.PathEscape(cid)+"/start", nil, nil)
	if err != nil {
		return nil, err
	}
	sBody, _ := io.ReadAll(io.LimitReader(sResp.Body, 16<<10))
	_ = sResp.Body.Close()
	if sResp.StatusCode < 200 || sResp.StatusCode >= 300 {
		return nil, fmt.Errorf("start host config helper failed (HTTP %d): %s", sResp.StatusCode, strings.TrimSpace(string(sBody)))
	}

	wResp, err := l.RequestDocker(ctx, http.MethodPost, "/containers/"+url.PathEscape(cid)+"/wait", nil, nil)
	if err != nil {
		return nil, err
	}
	wBody, _ := io.ReadAll(io.LimitReader(wResp.Body, 16<<10))
	_ = wResp.Body.Close()
	var waitRes struct {
		StatusCode int `json:"StatusCode"`
	}
	_ = json.Unmarshal(wBody, &waitRes)

	var stdout, stderr []byte
	if lResp, lErr := l.RequestDocker(ctx, http.MethodGet, "/containers/"+url.PathEscape(cid)+"/logs?stdout=1&stderr=1", nil, nil); lErr == nil && lResp != nil {
		stdout, stderr = demuxDockerLogs(lResp.Body)
		_ = lResp.Body.Close()
	}
	if waitRes.StatusCode != 0 {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = strings.TrimSpace(string(stdout))
		}
		if msg != "" {
			return stdout, fmt.Errorf("exit status %d (%s)", waitRes.StatusCode, msg)
		}
		return stdout, fmt.Errorf("exit status %d", waitRes.StatusCode)
	}
	return stdout, nil
}

// InspectDockerDaemonConfig 探测本机（或容器所属宿主机）实际生效的 dockerd 配置文件及 OpenWrt UCI 配置。
func (l *LocalTransport) InspectDockerDaemonConfig(ctx context.Context) (*HostDaemonConfigState, error) {
	ictx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if isRunningInsideContainer() {
		out, err := l.runContainerHostScript(ictx, buildInspectDaemonConfigScript("/host_root"))
		if err != nil {
			return nil, err
		}
		return parseInspectDaemonConfigOutput(string(out))
	}
	cmd := exec.CommandContext(ictx, "sh", "-c", buildInspectDaemonConfigScript(""))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(out)))
	}
	return parseInspectDaemonConfigOutput(string(out))
}

// ApplyDockerDaemonConfig 在本机（或容器所属宿主机）原子写入镜像源配置并热重载 dockerd。
func (l *LocalTransport) ApplyDockerDaemonConfig(ctx context.Context, plan ApplyDaemonMirrorPlan) error {
	actx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if isRunningInsideContainer() {
		_, err := l.runContainerHostScript(actx, buildApplyDaemonConfigScript("/host_root", plan))
		return err
	}
	cmd := exec.CommandContext(actx, "sh", "-c", buildApplyDaemonConfigScript("", plan))
	out, err := cmd.CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%w (%s)", err, msg)
		}
		return err
	}
	return nil
}

// InspectDockerDaemonConfig 通过 SSH（含自动 sudo 提权回退）探测远端主机实际生效的 dockerd 配置文件及 OpenWrt UCI 配置。
func (t *SSHTransport) InspectDockerDaemonConfig(ctx context.Context) (*HostDaemonConfigState, error) {
	if err := t.ensureClient(ctx); err != nil {
		return nil, err
	}
	ictx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	out, err := t.runHostProbeWithSudoFallback(ictx, buildInspectDaemonConfigScript(""), func(s string, e error) bool {
		if e != nil {
			return true
		}
		_, pErr := parseInspectDaemonConfigOutput(s)
		return pErr != nil
	})
	if err != nil {
		return nil, err
	}
	return parseInspectDaemonConfigOutput(out)
}

// ApplyDockerDaemonConfig 通过 SSH（含自动 sudo 提权回退）原子写入远端主机镜像源配置并热重载 dockerd。
func (t *SSHTransport) ApplyDockerDaemonConfig(ctx context.Context, plan ApplyDaemonMirrorPlan) error {
	if err := t.ensureClient(ctx); err != nil {
		return err
	}
	actx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	script := buildApplyDaemonConfigScript("", plan)
	out, err := t.runPrivilegedCmd(actx, "sh -c "+shellQuote(script), nil)
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%w (%s)", err, msg)
		}
		return err
	}
	return nil
}

// ReloadDockerDaemon 在本机通过 SIGHUP 热重载 dockerd 配置（不中断运行中容器）。
func (l *LocalTransport) ReloadDockerDaemon(ctx context.Context) error {
	rctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if isRunningInsideContainer() {
		if _, err := l.runContainerHostScript(rctx, buildReloadDockerScript("/host_root")); err != nil {
			return fmt.Errorf("reload docker daemon: %w", err)
		}
		return nil
	}
	cmd := exec.CommandContext(rctx, "sh", "-c", buildReloadDockerScript(""))
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
	out, err := t.runPrivilegedCmd(rctx, "sh -c "+shellQuote(buildReloadDockerScript("")), nil)
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

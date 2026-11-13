// Package transport 提供与目标主机通信的只读传输层（local socket / ssh）。
package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
	"golang.org/x/crypto/ssh"
)

// Mode 表示 SSH 访问远端 Docker Engine 的具体路径。
type Mode string

const (
	ModeLocal         Mode = "local"
	ModeStreamLocal   Mode = "streamlocal"
	ModeDialStdio     Mode = "dial_stdio"
	ModeSudoStdio     Mode = "sudo_dial_stdio"
	ModeSudoPassStdio Mode = "sudo_pass_dial_stdio"
)

// ErrFileConflict 表示目标文件在更新执行前已被外部修改。
var ErrFileConflict = errors.New("external file conflict")

// Transport 封装到一台目标主机的基础连接。
type Transport interface {
	// Mode 返回实际生效的传输模式。
	Mode() Mode
	// DoDocker 向目标 Docker Engine 发起只读 HTTP 请求（GET/HEAD）。
	DoDocker(ctx context.Context, path string) (*http.Response, error)
	// ReadFile 读取目标主机上的文本文件（用于读取 Compose YAML 与 .env，上限 2 MiB）。
	ReadFile(ctx context.Context, path string) ([]byte, error)
	Close() error
}

// ExecutableTransport 扩展 Transport，提供镜像拉取、容器重建、Compose 单服务更新、整项目配置更新与冲突校验写入能力。
type ExecutableTransport interface {
	Transport
	RequestDocker(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error)
	WriteFileAtomic(ctx context.Context, path string, data []byte, expectedSHA256 string) error
	ExecComposeService(ctx context.Context, workDir string, configFiles, envFiles []string, projectName, serviceName string, startContainer bool) (string, error)
	ExecComposeProject(ctx context.Context, workDir string, configFiles, envFiles []string, projectName string, pullAndRemoveOrphans bool) (string, error)
}

// StreamingComposeTransport 扩展 ExecutableTransport，支持实时流式回调 docker compose 命令产生的增量终端输出（附录 E.1 / E.3）。
type StreamingComposeTransport interface {
	ExecutableTransport
	ExecComposeServiceStream(ctx context.Context, workDir string, configFiles, envFiles []string, projectName, serviceName string, startContainer bool, onChunk func(string)) (string, error)
	ExecComposeProjectStream(ctx context.Context, workDir string, configFiles, envFiles []string, projectName string, pullAndRemoveOrphans bool, onChunk func(string)) (string, error)
}

// CleanupTransport 扩展 ExecutableTransport，提供 Compose 整项目下线与受保护的宿主机路径清理能力。
type CleanupTransport interface {
	ExecutableTransport
	ExecComposeDown(ctx context.Context, workDir string, configFiles, envFiles []string, projectName string, removeVolumes bool) (string, error)
	RemoveHostPaths(ctx context.Context, paths []string) error
}

const maxFileSize = 2 << 20 // 2 MiB

// newReadOnlyHTTPClient 使用自定义 dialer 构建默认 30s 超时的 Docker HTTP 客户端。
func newReadOnlyHTTPClient(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:         dial,
			DisableKeepAlives:   false,
			MaxIdleConns:        32,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     45 * time.Second,
		},
		Timeout: 30 * time.Second,
	}
}

// newStreamingHTTPClient 构建不设固定总超时（由 ctx 控制生命周期）的 Docker HTTP 客户端，用于镜像拉取流与容器操作。
func newStreamingHTTPClient(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:         dial,
			DisableKeepAlives:   false,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     30 * time.Second,
		},
	}
}

// LocalTransport 通过本机 Unix Socket 访问 Docker，并直接读写本机文件。
type LocalTransport struct {
	socketPath   string
	httpClient   *http.Client
	streamClient *http.Client
}

// NewLocal 创建本机传输。socketPath 为空时使用默认路径。
func NewLocal(socketPath string) *LocalTransport {
	if socketPath == "" {
		socketPath = DefaultLocalSocket()
	}
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socketPath)
	}
	return &LocalTransport{
		socketPath:   socketPath,
		httpClient:   newReadOnlyHTTPClient(dial),
		streamClient: newStreamingHTTPClient(dial),
	}
}

// DefaultLocalSocket 返回默认 Docker Unix Socket 路径。
func DefaultLocalSocket() string {
	if v := os.Getenv("DKSTORE_DOCKER_SOCKET"); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		return `/var/run/docker.sock`
	}
	return "/var/run/docker.sock"
}

func (l *LocalTransport) Mode() Mode { return ModeLocal }

func (l *LocalTransport) DoDocker(ctx context.Context, path string) (*http.Response, error) {
	return l.RequestDocker(ctx, http.MethodGet, path, nil, nil)
}

func (l *LocalTransport) RequestDocker(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, body)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if method == http.MethodGet || method == http.MethodHead {
		return l.httpClient.Do(req)
	}
	return l.streamClient.Do(req)
}

func (l *LocalTransport) ReadFile(_ context.Context, path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxFileSize))
}

func (l *LocalTransport) WriteFileAtomic(ctx context.Context, path string, data []byte, expectedSHA256 string) error {
	if expectedSHA256 != "" {
		cur, err := l.ReadFile(ctx, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(cur)
		if hex.EncodeToString(sum[:]) != expectedSHA256 {
			return ErrFileConflict
		}
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp := path + ".dkstore.tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (l *LocalTransport) ExecComposeService(ctx context.Context, workDir string, configFiles, envFiles []string, projectName, serviceName string, startContainer bool) (string, error) {
	return l.ExecComposeServiceStream(ctx, workDir, configFiles, envFiles, projectName, serviceName, startContainer, nil)
}

func (l *LocalTransport) ExecComposeServiceStream(ctx context.Context, workDir string, configFiles, envFiles []string, projectName, serviceName string, startContainer bool, onChunk func(string)) (string, error) {
	args := buildComposeArgs(workDir, configFiles, envFiles, projectName, serviceName, startContainer)
	env := append(os.Environ(), "PATH="+os.Getenv("PATH")+":/usr/local/bin:/var/packages/ContainerManager/target/usr/bin:/var/packages/Docker/target/usr/bin")
	newCmd := func(name string, a ...string) *exec.Cmd {
		c := exec.CommandContext(ctx, name, a...)
		if workDir != "" {
			c.Dir = workDir
		}
		c.Env = env
		return c
	}
	// 仅当 docker compose 插件不存在时才回退 docker-compose；真实执行失败不重复执行（审计 L4）
	name, cmdArgs := "docker", append([]string{"compose"}, args...)
	if err := newCmd("docker", "compose", "version").Run(); err != nil {
		if _, lerr := exec.LookPath("docker-compose"); lerr == nil {
			name, cmdArgs = "docker-compose", args
		}
	}
	cmd := newCmd(name, cmdArgs...)
	buf := &syncBuffer{onChunk: onChunk}
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()
	outStr := strings.TrimSpace(string(buf.Bytes()))
	if err != nil {
		return outStr, fmt.Errorf("%s compose: %w (%s)", name, err, outStr)
	}
	return outStr, nil
}

func (l *LocalTransport) ExecComposeProject(ctx context.Context, workDir string, configFiles, envFiles []string, projectName string, pullAndRemoveOrphans bool) (string, error) {
	return l.ExecComposeProjectStream(ctx, workDir, configFiles, envFiles, projectName, pullAndRemoveOrphans, nil)
}

func (l *LocalTransport) ExecComposeProjectStream(ctx context.Context, workDir string, configFiles, envFiles []string, projectName string, pullAndRemoveOrphans bool, onChunk func(string)) (string, error) {
	baseFlags := buildComposeBaseFlags(workDir, configFiles, envFiles, projectName)
	env := append(os.Environ(), "PATH="+os.Getenv("PATH")+":/usr/local/bin:/var/packages/ContainerManager/target/usr/bin:/var/packages/Docker/target/usr/bin")
	newCmd := func(name string, a ...string) *exec.Cmd {
		c := exec.CommandContext(ctx, name, a...)
		if workDir != "" {
			c.Dir = workDir
		}
		c.Env = env
		return c
	}
	usePlugin := newCmd("docker", "compose", "version").Run() == nil
	runSub := func(subArgs ...string) (string, error) {
		full := append(append([]string(nil), baseFlags...), subArgs...)
		name, cmdArgs := "docker", append([]string{"compose"}, full...)
		if !usePlugin {
			if _, lerr := exec.LookPath("docker-compose"); lerr == nil {
				name, cmdArgs = "docker-compose", full
			}
		}
		cmd := newCmd(name, cmdArgs...)
		buf := &syncBuffer{onChunk: onChunk}
		cmd.Stdout = buf
		cmd.Stderr = buf
		err := cmd.Run()
		outStr := strings.TrimSpace(string(buf.Bytes()))
		if err != nil {
			return outStr, fmt.Errorf("%s compose: %w (%s)", name, err, outStr)
		}
		return outStr, nil
	}
	var outputs []string
	if pullAndRemoveOrphans {
		if out, err := runSub("pull"); err != nil {
			return out, err
		} else if out != "" {
			outputs = append(outputs, out)
		}
		out, err := runSub("up", "-d", "--remove-orphans")
		if err != nil {
			return out, err
		}
		if out != "" {
			outputs = append(outputs, out)
		}
		return strings.Join(outputs, "\n"), nil
	}
	return runSub("up", "-d")
}

func (l *LocalTransport) ExecComposeDown(ctx context.Context, workDir string, configFiles, envFiles []string, projectName string, removeVolumes bool) (string, error) {
	baseFlags := buildComposeBaseFlags(workDir, configFiles, envFiles, projectName)
	downArgs := []string{"down", "--remove-orphans"}
	if removeVolumes {
		downArgs = append(downArgs, "-v")
	}
	full := append(append([]string(nil), baseFlags...), downArgs...)
	env := append(os.Environ(), "PATH="+os.Getenv("PATH")+":/usr/local/bin:/var/packages/ContainerManager/target/usr/bin:/var/packages/Docker/target/usr/bin")
	newCmd := func(name string, a ...string) *exec.Cmd {
		c := exec.CommandContext(ctx, name, a...)
		if workDir != "" {
			if st, err := os.Stat(workDir); err == nil && st.IsDir() {
				c.Dir = workDir
			}
		}
		c.Env = env
		return c
	}
	name, cmdArgs := "docker", append([]string{"compose"}, full...)
	if newCmd("docker", "compose", "version").Run() != nil {
		if _, lerr := exec.LookPath("docker-compose"); lerr == nil {
			name, cmdArgs = "docker-compose", full
		}
	}
	cmd := newCmd(name, cmdArgs...)
	var buf syncBuffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	outStr := strings.TrimSpace(string(buf.Bytes()))
	if err != nil {
		return outStr, fmt.Errorf("%s compose down: %w (%s)", name, err, outStr)
	}
	return outStr, nil
}

func (l *LocalTransport) RemoveHostPaths(_ context.Context, paths []string) error {
	for _, raw := range paths {
		cleaned := platform.NormalizeHostPath(raw)
		if cleaned == "" || platform.IsProtectedHostPath(cleaned) {
			return i18n.NewEngineError(i18n.MsgTransportInvalidPath)
		}
		if err := os.RemoveAll(filepath.FromSlash(cleaned)); err != nil {
			return err
		}
	}
	return nil
}

func (l *LocalTransport) Close() error {
	l.httpClient.CloseIdleConnections()
	if l.streamClient != nil {
		l.streamClient.CloseIdleConnections()
	}
	return nil
}

// SSHConfig 是建立 SSH 连接所需的配置。
type SSHConfig struct {
	Address        string // host 或 IP
	Port           int    // 默认 22
	User           string // SSH 用户名
	AuthType       string // "password" | "key"
	Secret         string // 密码或 PEM 私钥原文
	Passphrase     string // 私钥口令（可选）
	TrustedHostKey string // 已确认的指纹（SHA256:...）；为空且 AllowTOFU=true 时首次自动记录
	AllowTOFU      bool   // 连接测试阶段允许捕获未确认的指纹
	PreferredMode  Mode   // 已探测过的传输模式（可为空）
	SocketPath     string // 远端 docker.sock，默认 /var/run/docker.sock
}

// HostKeyMismatchError 在远端主机公钥与已保存指纹不一致时返回。
type HostKeyMismatchError struct {
	Expected  string
	Presented string
}

func (e *HostKeyMismatchError) Error() string {
	return i18n.EngineMessage(i18n.ZhCN, i18n.MsgTransportHostKeyMismatch, e.Expected, e.Presented)
}

func (e *HostKeyMismatchError) Localize(lang i18n.Lang) string {
	return i18n.EngineMessage(lang, i18n.MsgTransportHostKeyMismatch, e.Expected, e.Presented)
}

// SSHTransport 通过 SSH 访问远端 Docker 与配置文件。
type SSHTransport struct {
	cfg          SSHConfig
	mu           sync.Mutex // 保护 client / closed / httpClient / streamClient / presentedKey
	client       *ssh.Client
	closed       bool // Close 之后不再允许 ensureClient 重新建立连接（审计 M8）
	mode         Mode
	presentedKey string // SHA256:... 指纹
	httpClient   *http.Client
	streamClient *http.Client
}

// ErrTransportClosed 表示传输已被关闭，不再重新连接。
var ErrTransportClosed = errors.New("transport closed")

// sshHandshakeTimeout 是 SSH 握手与认证阶段的最长等待时间（审计 M7）。
var sshHandshakeTimeout = 15 * time.Second

// DialSSH 建立 SSH 连接；若 PreferredMode 为空则自动按三级顺序探测 Docker 访问方式。
func DialSSH(ctx context.Context, cfg SSHConfig) (*SSHTransport, error) {
	if cfg.Port <= 0 {
		cfg.Port = 22
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = "/var/run/docker.sock"
	}
	t := &SSHTransport{cfg: cfg, mode: cfg.PreferredMode}
	if err := t.ensureClient(ctx); err != nil {
		return nil, err
	}
	t.httpClient = newReadOnlyHTTPClient(t.dialDockerConn)
	t.streamClient = newStreamingHTTPClient(t.dialDockerConn)
	if t.mode == "" {
		if err := t.probeDockerMode(ctx); err != nil {
			_ = t.Close()
			return nil, err
		}
	}
	return t, nil
}

// PresentedHostKey 返回握手时远端出示的公钥指纹（SHA256:...）。
func (t *SSHTransport) PresentedHostKey() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.presentedKey
}

func (t *SSHTransport) Mode() Mode { return t.mode }

// getClient 在锁内读取当前 SSH 客户端快照。
func (t *SSHTransport) getClient() (*ssh.Client, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, ErrTransportClosed
	}
	if t.client == nil {
		return nil, ErrTransportClosed
	}
	return t.client, nil
}

func (t *SSHTransport) ensureClient(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return ErrTransportClosed
	}
	if t.client != nil {
		return nil
	}
	authMethod, err := buildSSHAuth(t.cfg)
	if err != nil {
		return err
	}
	hostKeyCb := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fp := FingerprintSHA256(key)
		t.presentedKey = fp // 回调在 ensureClient 持锁期间同步执行
		if t.cfg.TrustedHostKey == "" {
			if t.cfg.AllowTOFU {
				return nil
			}
			return i18n.NewEngineError(i18n.MsgTransportUntrustedKey, fp)
		}
		if t.cfg.TrustedHostKey != fp {
			return &HostKeyMismatchError{Expected: t.cfg.TrustedHostKey, Presented: fp}
		}
		return nil
	}
	addr := net.JoinHostPort(t.cfg.Address, fmt.Sprintf("%d", t.cfg.Port))
	d := net.Dialer{Timeout: sshHandshakeTimeout}
	rawConn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return i18n.NewEngineError(i18n.MsgTransportTCPFailed, addr, err.Error())
	}
	sshCfg := &ssh.ClientConfig{
		User:            t.cfg.User,
		Auth:            []ssh.AuthMethod{authMethod},
		HostKeyCallback: hostKeyCb,
		Timeout:         sshHandshakeTimeout,
	}
	c, chans, reqs, err := handshakeWithDeadline(ctx, rawConn, addr, sshCfg, sshHandshakeTimeout)
	if err != nil {
		var hke *HostKeyMismatchError
		if errors.As(err, &hke) {
			return hke
		}
		return i18n.NewEngineError(i18n.MsgTransportHandshakeFailed, i18n.DetailOf(err))
	}
	t.client = ssh.NewClient(c, chans, reqs)
	return nil
}

// handshakeWithDeadline 在 rawConn 上执行 SSH 握手：握手前设置超时 deadline，ctx 取消时立即中断，握手成功后清除 deadline（审计 M7）。
// 失败时负责关闭 rawConn。
func handshakeWithDeadline(ctx context.Context, rawConn net.Conn, addr string, cfg *ssh.ClientConfig, timeout time.Duration) (ssh.Conn, <-chan ssh.NewChannel, <-chan *ssh.Request, error) {
	_ = rawConn.SetDeadline(time.Now().Add(timeout))
	stop := context.AfterFunc(ctx, func() {
		// 设置一个过去的 deadline 让阻塞中的读写立即返回
		_ = rawConn.SetDeadline(time.Unix(1, 0))
	})
	c, chans, reqs, err := ssh.NewClientConn(rawConn, addr, cfg)
	notFired := stop()
	if err != nil {
		_ = rawConn.Close()
		if ctx.Err() != nil {
			return nil, nil, nil, ctx.Err()
		}
		return nil, nil, nil, err
	}
	if !notFired {
		_ = c.Close()
		return nil, nil, nil, ctx.Err()
	}
	_ = rawConn.SetDeadline(time.Time{})
	return c, chans, reqs, nil
}

func buildSSHAuth(cfg SSHConfig) (ssh.AuthMethod, error) {
	switch cfg.AuthType {
	case "key":
		var (
			signer ssh.Signer
			err    error
		)
		if cfg.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(cfg.Secret), []byte(cfg.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(cfg.Secret))
		}
		if err != nil {
			return nil, i18n.NewEngineError(i18n.MsgTransportParseKeyFailed, err.Error())
		}
		return ssh.PublicKeys(signer), nil
	case "password", "":
		return ssh.Password(cfg.Secret), nil
	default:
		return nil, i18n.NewEngineError(i18n.MsgTransportUnsupportedAuth, cfg.AuthType)
	}
}

// probeDockerMode 依次尝试：streamlocal 直连 → docker system dial-stdio → sudo -n dial-stdio → sudo -k -S dial-stdio（兼容群晖等管理员密码 sudo）。
func (t *SSHTransport) probeDockerMode(ctx context.Context) error {
	candidates := []Mode{ModeStreamLocal, ModeDialStdio, ModeSudoStdio}
	if t.cfg.AuthType != "key" && t.cfg.Secret != "" {
		candidates = append(candidates, ModeSudoPassStdio)
	}
	var errs []string
	for _, m := range candidates {
		t.mu.Lock()
		t.mode = m
		// 替换只读客户端前关闭旧客户端的空闲连接，避免上一模式的 SSH 通道/会话泄漏（审计 M8）
		if old := t.httpClient; old != nil {
			old.CloseIdleConnections()
		}
		if t.streamClient != nil {
			t.streamClient.CloseIdleConnections()
		}
		t.httpClient = newReadOnlyHTTPClient(t.dialDockerConn)
		t.mu.Unlock()
		pCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		resp, err := t.DoDocker(pCtx, "/_ping")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			cancel()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			errs = append(errs, fmt.Sprintf("%s: HTTP %d", m, resp.StatusCode))
		} else {
			cancel()
			errs = append(errs, fmt.Sprintf("%s: %v", m, err))
		}
	}
	return i18n.NewEngineError(i18n.MsgTransportDockerProbeFailed, strings.Join(errs, "; "))
}

// streamLocalChannelPayload 对应 OpenSSH direct-streamlocal@openssh.com 请求结构。
type streamLocalChannelPayload struct {
	SocketPath string
	Reserved0  string
	Reserved1  uint32
}

const dockerStdioShellCmd = `sh -c 'PATH="$PATH:/usr/local/bin:/var/packages/ContainerManager/target/usr/bin:/var/packages/Docker/target/usr/bin" exec docker system dial-stdio'`

func (t *SSHTransport) dialDockerConn(ctx context.Context, _, _ string) (net.Conn, error) {
	if err := t.ensureClient(ctx); err != nil {
		return nil, err
	}
	cli, err := t.getClient()
	if err != nil {
		return nil, err
	}
	switch t.mode {
	case ModeStreamLocal:
		payload := ssh.Marshal(&streamLocalChannelPayload{SocketPath: t.cfg.SocketPath})
		ch, reqs, err := cli.OpenChannel("direct-streamlocal@openssh.com", payload)
		if err != nil {
			return nil, err
		}
		go ssh.DiscardRequests(reqs)
		return &sshChannelConn{Channel: ch}, nil
	case ModeDialStdio:
		return openStdioConn(cli, dockerStdioShellCmd, "")
	case ModeSudoStdio:
		return openStdioConn(cli, "sudo -n "+dockerStdioShellCmd, "")
	case ModeSudoPassStdio:
		return openStdioConn(cli, "sudo -k -S -p '' "+dockerStdioShellCmd, t.cfg.Secret)
	default:
		return nil, i18n.NewEngineError(i18n.MsgTransportUnknownMode, string(t.mode))
	}
}

func openStdioConn(cli *ssh.Client, cmd string, sudoPass string) (net.Conn, error) {
	sess, err := cli.NewSession()
	if err != nil {
		return nil, err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	if err := sess.Start(cmd); err != nil {
		_ = sess.Close()
		return nil, err
	}
	if sudoPass != "" {
		if _, err := io.WriteString(stdin, sudoPass+"\n"); err != nil {
			_ = sess.Close()
			return nil, err
		}
	}
	return &stdioSessionConn{sess: sess, r: stdout, w: stdin}, nil
}

func (t *SSHTransport) DoDocker(ctx context.Context, path string) (*http.Response, error) {
	return t.RequestDocker(ctx, http.MethodGet, path, nil, nil)
}

func (t *SSHTransport) RequestDocker(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, body)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, ErrTransportClosed
	}
	var hc *http.Client
	if method == http.MethodGet || method == http.MethodHead {
		hc = t.httpClient
	} else {
		if t.streamClient == nil {
			t.streamClient = newStreamingHTTPClient(t.dialDockerConn)
		}
		hc = t.streamClient
	}
	t.mu.Unlock()
	return hc.Do(req)
}

func (t *SSHTransport) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return nil, i18n.NewEngineError(i18n.MsgTransportInvalidPath)
	}
	if err := t.ensureClient(ctx); err != nil {
		return nil, err
	}
	baseCmd := "cat -- " + shellQuote(path)
	// 通过 LimitReader 流式读取，最多读取 maxFileSize 字节（审计 L12）
	out, err := t.runReadOnlyCmd(ctx, baseCmd, maxFileSize)
	if err != nil {
		return nil, i18n.NewEngineError(i18n.MsgTransportReadFileFailed, path, err.Error())
	}
	return out, nil
}

// writeFileScript 生成通过 stdin 接收文件内容并原子替换目标文件的 shell 脚本（全部路径经 shellQuote 处理）。
// 先尝试 cp -p 复制原文件以保留权限与属主，再用 stdin 覆盖临时文件内容，最后 mv 原子替换。
func writeFileScript(path string) string {
	tmpPath := path + ".dkstore.tmp"
	return fmt.Sprintf("{ cp -p -- %s %s 2>/dev/null || :; } && cat > %s && mv -f -- %s %s",
		shellQuote(path), shellQuote(tmpPath), shellQuote(tmpPath), shellQuote(tmpPath), shellQuote(path))
}

func (t *SSHTransport) WriteFileAtomic(ctx context.Context, path string, data []byte, expectedSHA256 string) error {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return i18n.NewEngineError(i18n.MsgTransportInvalidPath)
	}
	if err := t.ensureClient(ctx); err != nil {
		return err
	}
	if expectedSHA256 != "" {
		cur, err := t.ReadFile(ctx, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(cur)
		if hex.EncodeToString(sum[:]) != expectedSHA256 {
			return ErrFileConflict
		}
	}
	// 文件内容通过 stdin 传输，不放入命令参数，避免超过 ARG_MAX（审计 L3）
	out, err := t.runPrivilegedCmd(ctx, "sh -c "+shellQuote(writeFileScript(path)), data)
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%w (%s)", err, msg)
		}
		return err
	}
	return nil
}

// composeShellScript 生成远端 Compose 执行脚本：仅当 docker compose 插件不存在时才回退 docker-compose，
// 真实执行失败不会重复执行（审计 L4）。
func composeShellScript(workDir string, args []string) string {
	quotedArgs := make([]string, 0, len(args))
	for _, a := range args {
		quotedArgs = append(quotedArgs, shellQuote(a))
	}
	argStr := strings.Join(quotedArgs, " ")
	cdPart := ""
	if workDir != "" {
		cdPart = "cd -- " + shellQuote(workDir) + " && "
	}
	return fmt.Sprintf(`PATH="$PATH:/usr/local/bin:/var/packages/ContainerManager/target/usr/bin:/var/packages/Docker/target/usr/bin"; %s`+
		`if docker compose version >/dev/null 2>&1; then docker compose %s 2>&1; `+
		`elif command -v docker-compose >/dev/null 2>&1; then docker-compose %s 2>&1; `+
		`else echo 'docker compose: command not found' >&2; exit 127; fi`,
		cdPart, argStr, argStr)
}

func (t *SSHTransport) ExecComposeService(ctx context.Context, workDir string, configFiles, envFiles []string, projectName, serviceName string, startContainer bool) (string, error) {
	return t.ExecComposeServiceStream(ctx, workDir, configFiles, envFiles, projectName, serviceName, startContainer, nil)
}

func (t *SSHTransport) ExecComposeServiceStream(ctx context.Context, workDir string, configFiles, envFiles []string, projectName, serviceName string, startContainer bool, onChunk func(string)) (string, error) {
	if err := t.ensureClient(ctx); err != nil {
		return "", err
	}
	args := buildComposeArgs(workDir, configFiles, envFiles, projectName, serviceName, startContainer)
	out, err := t.runPrivilegedCmdStream(ctx, "sh -c "+shellQuote(composeShellScript(workDir, args)), nil, onChunk)
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("remote docker compose: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func (t *SSHTransport) ExecComposeProject(ctx context.Context, workDir string, configFiles, envFiles []string, projectName string, pullAndRemoveOrphans bool) (string, error) {
	return t.ExecComposeProjectStream(ctx, workDir, configFiles, envFiles, projectName, pullAndRemoveOrphans, nil)
}

func (t *SSHTransport) ExecComposeProjectStream(ctx context.Context, workDir string, configFiles, envFiles []string, projectName string, pullAndRemoveOrphans bool, onChunk func(string)) (string, error) {
	if err := t.ensureClient(ctx); err != nil {
		return "", err
	}
	baseFlags := buildComposeBaseFlags(workDir, configFiles, envFiles, projectName)
	var outputs []string
	runSub := func(subArgs ...string) (string, error) {
		args := append(append([]string(nil), baseFlags...), subArgs...)
		out, err := t.runPrivilegedCmdStream(ctx, "sh -c "+shellQuote(composeShellScript(workDir, args)), nil, onChunk)
		if err != nil {
			return strings.TrimSpace(string(out)), fmt.Errorf("remote docker compose: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	if pullAndRemoveOrphans {
		if out, err := runSub("pull"); err != nil {
			return out, err
		} else if out != "" {
			outputs = append(outputs, out)
		}
		out, err := runSub("up", "-d", "--remove-orphans")
		if err != nil {
			return out, err
		}
		if out != "" {
			outputs = append(outputs, out)
		}
		return strings.Join(outputs, "\n"), nil
	}
	return runSub("up", "-d")
}

// ExecSynoWebAPIProjectList 在群晖主机上通过只读 synowebapi 查询 Container Manager 原生项目列表。
func (t *SSHTransport) ExecSynoWebAPIProjectList(ctx context.Context) ([]byte, error) {
	if err := t.ensureClient(ctx); err != nil {
		return nil, err
	}
	const cmd = "/usr/syno/bin/synowebapi --exec api=SYNO.Docker.Project version=1 method=list"
	return t.runReadOnlyCmd(ctx, cmd, 0)
}

// ExecSynoWebAPIProjectDelete 在群晖 SSH 模式下同步清理并删除 Container Manager 原生项目注册记录。
func (t *SSHTransport) ExecSynoWebAPIProjectDelete(ctx context.Context, projectID string) error {
	id := strings.TrimSpace(projectID)
	if id == "" {
		return nil
	}
	if err := t.ensureClient(ctx); err != nil {
		return err
	}
	idJSON, _ := fmt.Sprintf("%q", id), error(nil)
	cleanCmd := fmt.Sprintf("/usr/syno/bin/synowebapi --exec api=SYNO.Docker.Project version=1 method=clean_stream id=%s >/dev/null 2>&1 || true; /usr/syno/bin/synowebapi --exec api=SYNO.Docker.Project version=1 method=delete id=%s",
		shellQuote(idJSON), shellQuote(idJSON))
	_, err := t.runPrivilegedCmd(ctx, "sh -c "+shellQuote(cleanCmd), nil)
	return err
}

func (t *SSHTransport) ExecComposeDown(ctx context.Context, workDir string, configFiles, envFiles []string, projectName string, removeVolumes bool) (string, error) {
	if err := t.ensureClient(ctx); err != nil {
		return "", err
	}
	baseFlags := buildComposeBaseFlags(workDir, configFiles, envFiles, projectName)
	downArgs := append(append([]string(nil), baseFlags...), "down", "--remove-orphans")
	if removeVolumes {
		downArgs = append(downArgs, "-v")
	}
	// 若宿主机目录已不存在，不强制 cd 失败，回退直接在任意目录按 -p projectName 执行
	script := composeShellScript(workDir, downArgs)
	if workDir != "" {
		fallbackScript := composeShellScript("", append([]string{"-p", projectName, "down", "--remove-orphans"}, func() []string {
			if removeVolumes {
				return []string{"-v"}
			}
			return nil
		}()...))
		script = fmt.Sprintf("if [ -d %s ]; then %s; else %s; fi", shellQuote(workDir), script, fallbackScript)
	}
	out, err := t.runPrivilegedCmd(ctx, "sh -c "+shellQuote(script), nil)
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("remote docker compose down: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func (t *SSHTransport) RemoveHostPaths(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	if err := t.ensureClient(ctx); err != nil {
		return err
	}
	quoted := make([]string, 0, len(paths))
	for _, raw := range paths {
		cleaned := platform.NormalizeHostPath(raw)
		if cleaned == "" || platform.IsProtectedHostPath(cleaned) {
			return i18n.NewEngineError(i18n.MsgTransportInvalidPath)
		}
		quoted = append(quoted, shellQuote(cleaned))
	}
	cmd := "rm -rf -- " + strings.Join(quoted, " ")
	out, err := t.runPrivilegedCmd(ctx, "sh -c "+shellQuote(cmd), nil)
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%w (%s)", err, msg)
		}
		return err
	}
	return nil
}

// runSession 在新 SSH 会话中执行命令。stdin 可为空；limit>0 时只读取前 limit 字节的标准输出（达到上限后结束会话）；
// combined=true 时合并 stdout 与 stderr。ctx 取消时关闭会话。
func (t *SSHTransport) runSession(ctx context.Context, cmd string, stdin io.Reader, limit int64, combined bool) ([]byte, error) {
	return t.runSessionStream(ctx, cmd, stdin, limit, combined, nil)
}

func (t *SSHTransport) runSessionStream(ctx context.Context, cmd string, stdin io.Reader, limit int64, combined bool, onChunk func(string)) ([]byte, error) {
	cli, err := t.getClient()
	if err != nil {
		return nil, err
	}
	sess, err := cli.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	if stdin != nil {
		sess.Stdin = stdin
	}
	stop := context.AfterFunc(ctx, func() { _ = sess.Close() })
	defer stop()

	if combined {
		buf := syncBuffer{onChunk: onChunk}
		sess.Stdout = &buf
		sess.Stderr = &buf
		err := sess.Run(cmd)
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		return buf.Bytes(), err
	}

	var stderr syncBuffer
	sess.Stderr = &stderr
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := sess.Start(cmd); err != nil {
		return nil, err
	}
	var r io.Reader = stdout
	if limit > 0 {
		r = io.LimitReader(stdout, limit)
	}
	data, rerr := io.ReadAll(r)
	if limit > 0 && int64(len(data)) >= limit {
		// 已读满上限：直接结束会话，丢弃剩余输出
		return data, nil
	}
	werr := sess.Wait()
	if ctx.Err() != nil {
		return data, ctx.Err()
	}
	if rerr != nil {
		return data, rerr
	}
	if werr != nil {
		if msg := strings.TrimSpace(string(stderr.Bytes())); msg != "" {
			return data, fmt.Errorf("%w: %s", werr, msg)
		}
		return data, werr
	}
	return data, nil
}

// sudoStdin 拼接 sudo 口令行与后续 stdin 内容（sudo -S 逐字节读取到换行为止，其余内容交给被执行命令）。
func sudoStdin(pass string, payload []byte) io.Reader {
	b := make([]byte, 0, len(pass)+1+len(payload))
	b = append(b, pass...)
	b = append(b, '\n')
	b = append(b, payload...)
	return bytes.NewReader(b)
}

func (t *SSHTransport) runReadOnlyCmd(ctx context.Context, baseCmd string, limit int64) ([]byte, error) {
	out, err := t.runSession(ctx, baseCmd, nil, limit, false)
	if err == nil {
		return out, nil
	}
	if ctx.Err() != nil || errors.Is(err, ErrTransportClosed) {
		return nil, err
	}
	switch {
	case t.mode == ModeSudoStdio:
		return t.runSession(ctx, "sudo -n "+baseCmd, nil, limit, false)
	case t.mode == ModeSudoPassStdio && t.cfg.Secret != "":
		return t.runSession(ctx, "sudo -k -S -p '' "+baseCmd, sudoStdin(t.cfg.Secret, nil), limit, false)
	default:
		return nil, err
	}
}

func (t *SSHTransport) runPrivilegedCmd(ctx context.Context, baseCmd string, payload []byte) ([]byte, error) {
	return t.runPrivilegedCmdStream(ctx, baseCmd, payload, nil)
}

func (t *SSHTransport) runPrivilegedCmdStream(ctx context.Context, baseCmd string, payload []byte, onChunk func(string)) ([]byte, error) {
	var plain io.Reader
	if payload != nil {
		plain = bytes.NewReader(payload)
	}
	if t.mode == ModeSudoStdio {
		return t.runSessionStream(ctx, "sudo -n "+baseCmd, plain, 0, true, onChunk)
	}
	if t.mode == ModeSudoPassStdio && t.cfg.Secret != "" {
		return t.runSessionStream(ctx, "sudo -k -S -p '' "+baseCmd, sudoStdin(t.cfg.Secret, payload), 0, true, onChunk)
	}
	return t.runSessionStream(ctx, baseCmd, plain, 0, true, onChunk)
}

// Close 关闭 SSH 连接并标记为已关闭；之后的任何请求都不会重新建立连接（审计 M8）。
func (t *SSHTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	if t.httpClient != nil {
		t.httpClient.CloseIdleConnections()
	}
	if t.streamClient != nil {
		t.streamClient.CloseIdleConnections()
	}
	if t.client != nil {
		err := t.client.Close()
		t.client = nil
		return err
	}
	return nil
}

// syncBuffer 是并发安全的 bytes.Buffer（SSH/本地会话会在独立协程中写入 stdout/stderr，并支持实时增量回调）。
type syncBuffer struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	onChunk func(string)
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buf.Write(p)
	cb := b.onChunk
	b.mu.Unlock()
	if cb != nil && n > 0 {
		cb(string(p[:n]))
	}
	return n, err
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func buildComposeBaseFlags(workDir string, configFiles, envFiles []string, projectName string) []string {
	var args []string
	if projectName != "" {
		args = append(args, "-p", projectName)
	}
	for _, cf := range configFiles {
		p := cf
		if workDir != "" && !strings.HasPrefix(p, "/") && !(len(p) >= 2 && p[1] == ':') {
			p = filepath.ToSlash(filepath.Join(workDir, p))
		}
		args = append(args, "-f", p)
	}
	for _, ef := range envFiles {
		p := ef
		if workDir != "" && !strings.HasPrefix(p, "/") && !(len(p) >= 2 && p[1] == ':') {
			p = filepath.ToSlash(filepath.Join(workDir, p))
		}
		args = append(args, "--env-file", p)
	}
	return args
}

func buildComposeArgs(workDir string, configFiles, envFiles []string, projectName, serviceName string, startContainer bool) []string {
	args := buildComposeBaseFlags(workDir, configFiles, envFiles, projectName)
	if startContainer {
		args = append(args, "up", "-d", "--no-deps", "--", serviceName)
	} else {
		args = append(args, "up", "--no-start", "--no-deps", "--", serviceName)
	}
	return args
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// FingerprintSHA256 计算 SSH 公钥的 SHA256 指纹（与 OpenSSH 显示格式一致）。
func FingerprintSHA256(pub ssh.PublicKey) string {
	sum := sha256.Sum256(pub.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// GenerateEd25519KeyPair 生成一对 Ed25519 SSH 密钥（PEM 私钥 + authorized_keys 公钥行）。
func GenerateEd25519KeyPair(comment string) (privatePEM, authorizedKey string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	pemBlock, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	authLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if comment != "" {
		authLine += " " + comment
	}
	return string(pem.EncodeToMemory(pemBlock)), authLine, nil
}

type sshChannelConn struct {
	ssh.Channel
}

func (c *sshChannelConn) LocalAddr() net.Addr                { return dummyAddr("ssh-local") }
func (c *sshChannelConn) RemoteAddr() net.Addr               { return dummyAddr("ssh-remote") }
func (c *sshChannelConn) SetDeadline(_ time.Time) error      { return nil }
func (c *sshChannelConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *sshChannelConn) SetWriteDeadline(_ time.Time) error { return nil }

type stdioSessionConn struct {
	sess *ssh.Session
	r    io.Reader
	w    io.WriteCloser
}

func (c *stdioSessionConn) Read(b []byte) (int, error)  { return c.r.Read(b) }
func (c *stdioSessionConn) Write(b []byte) (int, error) { return c.w.Write(b) }
func (c *stdioSessionConn) Close() error {
	_ = c.w.Close()
	return c.sess.Close()
}
func (c *stdioSessionConn) LocalAddr() net.Addr                { return dummyAddr("stdio-local") }
func (c *stdioSessionConn) RemoteAddr() net.Addr               { return dummyAddr("stdio-remote") }
func (c *stdioSessionConn) SetDeadline(_ time.Time) error      { return nil }
func (c *stdioSessionConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *stdioSessionConn) SetWriteDeadline(_ time.Time) error { return nil }

type dummyAddr string

func (d dummyAddr) Network() string { return string(d) }
func (d dummyAddr) String() string  { return string(d) }

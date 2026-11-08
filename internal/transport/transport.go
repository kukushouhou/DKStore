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

// ExecutableTransport 扩展 Transport，提供镜像拉取、容器重建、Compose 单服务更新与冲突校验写入能力。
type ExecutableTransport interface {
	Transport
	RequestDocker(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error)
	WriteFileAtomic(ctx context.Context, path string, data []byte, expectedSHA256 string) error
	ExecComposeService(ctx context.Context, workDir string, configFiles, envFiles []string, projectName, serviceName string, startContainer bool) (string, error)
}

const maxFileSize = 2 << 20 // 2 MiB

// newReadOnlyHTTPClient 使用自定义 dialer 构建默认 30s 超时的 Docker HTTP 客户端。
func newReadOnlyHTTPClient(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:         dial,
			DisableKeepAlives:   false,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     30 * time.Second,
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
	args := buildComposeArgs(workDir, configFiles, envFiles, projectName, serviceName, startContainer)
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose"}, args...)...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.Env = append(os.Environ(), "PATH="+os.Getenv("PATH")+":/usr/local/bin:/var/packages/ContainerManager/target/usr/bin:/var/packages/Docker/target/usr/bin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// 回退尝试 docker-compose
		cmd2 := exec.CommandContext(ctx, "docker-compose", args...)
		if workDir != "" {
			cmd2.Dir = workDir
		}
		cmd2.Env = cmd.Env
		if out2, err2 := cmd2.CombinedOutput(); err2 == nil {
			return strings.TrimSpace(string(out2)), nil
		}
		return strings.TrimSpace(string(out)), fmt.Errorf("docker compose 执行失败: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
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
	Expected string
	Presented string
}

func (e *HostKeyMismatchError) Error() string {
	return fmt.Sprintf("SSH 主机指纹不匹配：已保存 %s，远端返回 %s", e.Expected, e.Presented)
}

// SSHTransport 通过 SSH 访问远端 Docker 与配置文件。
type SSHTransport struct {
	cfg          SSHConfig
	mu           sync.Mutex
	client       *ssh.Client
	mode         Mode
	presentedKey string // SHA256:... 指纹
	httpClient   *http.Client
	streamClient *http.Client
}

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
func (t *SSHTransport) PresentedHostKey() string { return t.presentedKey }

func (t *SSHTransport) Mode() Mode { return t.mode }

func (t *SSHTransport) ensureClient(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.client != nil {
		return nil
	}
	authMethod, err := buildSSHAuth(t.cfg)
	if err != nil {
		return err
	}
	hostKeyCb := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fp := FingerprintSHA256(key)
		t.presentedKey = fp
		if t.cfg.TrustedHostKey == "" {
			if t.cfg.AllowTOFU {
				return nil
			}
			return fmt.Errorf("尚未信任该主机指纹（%s）", fp)
		}
		if t.cfg.TrustedHostKey != fp {
			return &HostKeyMismatchError{Expected: t.cfg.TrustedHostKey, Presented: fp}
		}
		return nil
	}
	addr := net.JoinHostPort(t.cfg.Address, fmt.Sprintf("%d", t.cfg.Port))
	var d net.Dialer
	rawConn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("SSH TCP 连接失败 (%s): %w", addr, err)
	}
	sshCfg := &ssh.ClientConfig{
		User:            t.cfg.User,
		Auth:            []ssh.AuthMethod{authMethod},
		HostKeyCallback: hostKeyCb,
		Timeout:         15 * time.Second,
	}
	c, chans, reqs, err := ssh.NewClientConn(rawConn, addr, sshCfg)
	if err != nil {
		_ = rawConn.Close()
		return fmt.Errorf("SSH 握手/认证失败: %w", err)
	}
	t.client = ssh.NewClient(c, chans, reqs)
	return nil
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
			return nil, fmt.Errorf("解析 SSH 私钥失败: %w", err)
		}
		return ssh.PublicKeys(signer), nil
	case "password", "":
		return ssh.Password(cfg.Secret), nil
	default:
		return nil, fmt.Errorf("不支持的 SSH 认证方式: %s", cfg.AuthType)
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
		t.mode = m
		t.httpClient = newReadOnlyHTTPClient(t.dialDockerConn)
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
	return fmt.Errorf("无法通过 SSH 访问远端 Docker（已尝试直连 Socket、dial-stdio、sudo dial-stdio）：%s", strings.Join(errs, "；"))
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
	switch t.mode {
	case ModeStreamLocal:
		payload := ssh.Marshal(&streamLocalChannelPayload{SocketPath: t.cfg.SocketPath})
		ch, reqs, err := t.client.OpenChannel("direct-streamlocal@openssh.com", payload)
		if err != nil {
			return nil, err
		}
		go ssh.DiscardRequests(reqs)
		return &sshChannelConn{Channel: ch}, nil
	case ModeDialStdio:
		return t.openStdioConn(dockerStdioShellCmd, "")
	case ModeSudoStdio:
		return t.openStdioConn("sudo -n "+dockerStdioShellCmd, "")
	case ModeSudoPassStdio:
		return t.openStdioConn("sudo -k -S -p '' "+dockerStdioShellCmd, t.cfg.Secret)
	default:
		return nil, fmt.Errorf("未知传输模式: %s", t.mode)
	}
}

func (t *SSHTransport) openStdioConn(cmd string, sudoPass string) (net.Conn, error) {
	sess, err := t.client.NewSession()
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
	if method == http.MethodGet || method == http.MethodHead {
		return t.httpClient.Do(req)
	}
	if t.streamClient == nil {
		t.streamClient = newStreamingHTTPClient(t.dialDockerConn)
	}
	return t.streamClient.Do(req)
}

func (t *SSHTransport) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return nil, errors.New("文件路径不合法")
	}
	if err := t.ensureClient(ctx); err != nil {
		return nil, err
	}
	baseCmd := "cat -- " + shellQuote(path)
	out, err := t.runReadOnlyCmd(baseCmd)
	if err != nil {
		return nil, fmt.Errorf("读取远端文件 %s 失败: %w", path, err)
	}
	if len(out) > maxFileSize {
		out = out[:maxFileSize]
	}
	return out, nil
}

func (t *SSHTransport) WriteFileAtomic(ctx context.Context, path string, data []byte, expectedSHA256 string) error {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return errors.New("文件路径不合法")
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
	b64 := base64.StdEncoding.EncodeToString(data)
	tmpPath := path + ".dkstore.tmp"
	inner := fmt.Sprintf("printf %%s %s | base64 -d > %s && mv -f -- %s %s",
		shellQuote(b64), shellQuote(tmpPath), shellQuote(tmpPath), shellQuote(path))
	_, err := t.runPrivilegedCmd("sh -c " + shellQuote(inner))
	return err
}

func (t *SSHTransport) ExecComposeService(ctx context.Context, workDir string, configFiles, envFiles []string, projectName, serviceName string, startContainer bool) (string, error) {
	if err := t.ensureClient(ctx); err != nil {
		return "", err
	}
	args := buildComposeArgs(workDir, configFiles, envFiles, projectName, serviceName, startContainer)
	quotedArgs := make([]string, 0, len(args))
	for _, a := range args {
		quotedArgs = append(quotedArgs, shellQuote(a))
	}
	argStr := strings.Join(quotedArgs, " ")
	cdPart := ""
	if workDir != "" {
		cdPart = "cd -- " + shellQuote(workDir) + " && "
	}
	inner := fmt.Sprintf(`PATH="$PATH:/usr/local/bin:/var/packages/ContainerManager/target/usr/bin:/var/packages/Docker/target/usr/bin"; %s(docker compose %s 2>&1 || docker-compose %s 2>&1)`,
		cdPart, argStr, argStr)
	out, err := t.runPrivilegedCmd("sh -c " + shellQuote(inner))
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("远端 docker compose 执行失败: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ExecSynoWebAPIProjectList 在群晖主机上通过只读 synowebapi 查询 Container Manager 原生项目列表。
func (t *SSHTransport) ExecSynoWebAPIProjectList(ctx context.Context) ([]byte, error) {
	if err := t.ensureClient(ctx); err != nil {
		return nil, err
	}
	const cmd = "/usr/syno/bin/synowebapi --exec api=SYNO.Docker.Project version=1 method=list"
	return t.runReadOnlyCmd(cmd)
}

func (t *SSHTransport) runReadOnlyCmd(baseCmd string) ([]byte, error) {
	runOnce := func(cmd string, stdinPass string) ([]byte, error) {
		sess, err := t.client.NewSession()
		if err != nil {
			return nil, err
		}
		defer sess.Close()
		if stdinPass != "" {
			sess.Stdin = bytes.NewReader([]byte(stdinPass + "\n"))
		}
		return sess.Output(cmd)
	}
	if out, err := runOnce(baseCmd, ""); err == nil {
		return out, nil
	} else if t.mode == ModeSudoStdio {
		return runOnce("sudo -n "+baseCmd, "")
	} else if t.mode == ModeSudoPassStdio && t.cfg.Secret != "" {
		return runOnce("sudo -k -S -p '' "+baseCmd, t.cfg.Secret)
	} else {
		return nil, err
	}
}

func (t *SSHTransport) runPrivilegedCmd(baseCmd string) ([]byte, error) {
	runOnce := func(cmd string, stdinPass string) ([]byte, error) {
		sess, err := t.client.NewSession()
		if err != nil {
			return nil, err
		}
		defer sess.Close()
		if stdinPass != "" {
			sess.Stdin = bytes.NewReader([]byte(stdinPass + "\n"))
		}
		return sess.CombinedOutput(cmd)
	}
	if t.mode == ModeSudoStdio {
		return runOnce("sudo -n "+baseCmd, "")
	}
	if t.mode == ModeSudoPassStdio && t.cfg.Secret != "" {
		return runOnce("sudo -k -S -p '' "+baseCmd, t.cfg.Secret)
	}
	return runOnce(baseCmd, "")
}

func (t *SSHTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
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

func buildComposeArgs(workDir string, configFiles, envFiles []string, projectName, serviceName string, startContainer bool) []string {
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

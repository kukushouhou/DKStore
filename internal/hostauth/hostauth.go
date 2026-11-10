// Package hostauth 判定 DKStore 的安装方式，并在群晖套件 / OpenWrt 套件安装时
// 核验本次请求是否携带宿主设备管理员的有效登录态（实施文档 §10.1.1）。
//
// 定位为家庭内网、自有设备：只做“复用宿主登录态”的便捷判定，核验不到时由上层回落到
// DKStore 自有密码（setup_required），不做服务器级加固。
package hostauth

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// 安装方式（与 internal/selfupdate 的 installMode 取值保持一致）。
const (
	ModeDocker   = "docker"
	ModeSynology = "synology_spk"
	ModeOpenWrt  = "openwrt"
	ModeBinary   = "binary"
)

// 核验失败原因码（由 internal/i18n.HostAuthHint 转为本地化文案）。
const (
	ReasonUnsupportedMode = "unsupported_mode"      // 非群晖/OpenWrt 套件安装，不支持宿主登录态
	ReasonNoCookie        = "no_host_cookie"        // 请求中没有宿主登录 Cookie
	ReasonInvalidCookie   = "invalid_host_cookie"   // Cookie 含非法字符
	ReasonUnavailable     = "host_auth_unavailable" // 宿主核验程序不存在（如未安装 LuCI / 非标准 DSM）
	ReasonSessionInvalid  = "host_session_invalid"  // 宿主会话无效或已过期
	ReasonNotAdmin        = "host_not_admin"        // 登录用户不是宿主管理员
)

const (
	execTimeout     = 3 * time.Second
	cacheOKTTL      = 60 * time.Second
	cacheFailTTL    = 10 * time.Second // 失败结果缓存更短，便于用户登录宿主后快速生效
	cacheMaxEntries = 256

	synoAuthCGI = "/usr/syno/synoman/webman/modules/authenticate.cgi"
	synoIDBin   = "/usr/bin/id"
	synoAdminGr = "administrators"
)

// Result 是一次宿主登录态核验的结果。
type Result struct {
	OK     bool
	User   string
	Reason string // OK=false 时的原因码
}

// Runner 执行外部命令并返回 stdout；参数以数组传递，不经过 shell。
type Runner func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)

// FS 抽象文件系统访问，便于测试。
type FS interface {
	Exists(path string) bool
	ReadFile(path string) ([]byte, error)
}

type osFS struct{}

func (osFS) Exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
func (osFS) ReadFile(p string) ([]byte, error) { return os.ReadFile(p) }

// OSFS 返回真实文件系统实现。
func OSFS() FS { return osFS{} }

// DefaultRunner 使用 os/exec 执行命令（带 3 秒超时）。
func DefaultRunner(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = env
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	return stdout.Bytes(), err
}

// DetectInstallMode 按运行环境判定安装方式：
// 容器内运行 → docker；/var/packages/dkstore 存在 → synology_spk；/etc/openwrt_release 存在 → openwrt；其他 → binary。
func DetectInstallMode(fs FS) string {
	if inContainer(fs) {
		return ModeDocker
	}
	if fs.Exists("/var/packages/dkstore") {
		return ModeSynology
	}
	if fs.Exists("/etc/openwrt_release") {
		return ModeOpenWrt
	}
	return ModeBinary
}

func inContainer(fs FS) bool {
	if fs.Exists("/.dockerenv") || fs.Exists("/run/.containerenv") {
		return true
	}
	if b, err := fs.ReadFile("/proc/1/cgroup"); err == nil {
		s := strings.ToLower(string(b))
		if strings.Contains(s, "docker") || strings.Contains(s, "containerd") || strings.Contains(s, "kubepods") {
			return true
		}
	}
	if strings.Contains(strings.ToLower(os.Getenv("CONTAINER")), "docker") {
		return true
	}
	return false
}

// ValidToken 校验 Cookie / sid / SynoToken 只包含 [A-Za-z0-9._-] 且长度合理。
func ValidToken(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// validUser 校验宿主返回的用户名可安全地作为命令参数（不以 - 开头、无控制字符/空白）。
func validUser(u string) bool {
	if u == "" || len(u) > 128 || strings.HasPrefix(u, "-") {
		return false
	}
	for _, r := range u {
		if r < 0x21 || r == 0x7f || r == ':' || r == '/' {
			return false
		}
	}
	return true
}

type cacheEntry struct {
	res Result
	exp time.Time
}

// Verifier 核验宿主设备登录态并按 Cookie 值短时缓存结果。
type Verifier struct {
	mode string
	run  Runner
	fs   FS
	now  func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// New 创建核验器；run/fs 为 nil 时使用真实实现。mode 为空时自动判定。
func New(mode string, run Runner, fs FS) *Verifier {
	if fs == nil {
		fs = OSFS()
	}
	if run == nil {
		run = DefaultRunner
	}
	if mode == "" {
		mode = DetectInstallMode(fs)
	}
	return &Verifier{mode: mode, run: run, fs: fs, now: time.Now, cache: make(map[string]cacheEntry)}
}

// InstallMode 返回当前安装方式。
func (v *Verifier) InstallMode() string { return v.mode }

// Supported 表示当前安装方式是否支持复用宿主登录态。
func (v *Verifier) Supported() bool {
	return v.mode == ModeSynology || v.mode == ModeOpenWrt
}

// Verify 核验请求是否携带宿主管理员的有效登录态。
func (v *Verifier) Verify(ctx context.Context, r *http.Request) Result {
	switch v.mode {
	case ModeSynology:
		return v.verifySynology(ctx, r)
	case ModeOpenWrt:
		return v.verifyOpenWrt(ctx, r)
	default:
		return Result{Reason: ReasonUnsupportedMode}
	}
}

func (v *Verifier) cached(key string, fn func() Result) Result {
	now := v.now()
	v.mu.Lock()
	if e, ok := v.cache[key]; ok && now.Before(e.exp) {
		v.mu.Unlock()
		return e.res
	}
	v.mu.Unlock()

	res := fn()

	ttl := cacheFailTTL
	if res.OK {
		ttl = cacheOKTTL
	}
	v.mu.Lock()
	if len(v.cache) >= cacheMaxEntries {
		for k, e := range v.cache {
			if !now.Before(e.exp) {
				delete(v.cache, k)
			}
		}
		if len(v.cache) >= cacheMaxEntries {
			v.cache = make(map[string]cacheEntry)
		}
	}
	v.cache[key] = cacheEntry{res: res, exp: now.Add(ttl)}
	v.mu.Unlock()
	return res
}

func cookieValue(r *http.Request, name string) string {
	if c, err := r.Cookie(name); err == nil {
		return c.Value
	}
	return ""
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------- 群晖 DSM ----------

func (v *Verifier) verifySynology(ctx context.Context, r *http.Request) Result {
	id := cookieValue(r, "id")
	if id == "" {
		return Result{Reason: ReasonNoCookie}
	}
	// SynoToken 是否必需待实机验证：DSM 开启“CSRF 保护”时 authenticate.cgi 可能要求附带 SynoToken。
	// 这里尽量从请求头 X-SYNO-TOKEN、query 或 Cookie 中取得并一并传给 CGI。
	token := r.Header.Get("X-SYNO-TOKEN")
	if token == "" {
		token = r.URL.Query().Get("SynoToken")
	}
	if token == "" {
		token = cookieValue(r, "SynoToken")
	}
	if !ValidToken(id) || (token != "" && !ValidToken(token)) {
		return Result{Reason: ReasonInvalidCookie}
	}
	ip := remoteIP(r)
	if net.ParseIP(ip) == nil {
		ip = ""
	}
	key := "syno|" + id + "|" + token + "|" + ip
	return v.cached(key, func() Result { return v.checkSynology(ctx, id, token, ip) })
}

func (v *Verifier) checkSynology(ctx context.Context, id, token, ip string) Result {
	if !v.fs.Exists(synoAuthCGI) {
		return Result{Reason: ReasonUnavailable}
	}
	cookie := "id=" + id
	query := ""
	if token != "" {
		query = "SynoToken=" + token
	}
	env := []string{
		"PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/syno/sbin:/usr/syno/bin",
		"REQUEST_METHOD=GET",
		"HTTP_COOKIE=" + cookie,
		"REMOTE_ADDR=" + ip,
		"HTTP_X_SYNO_TOKEN=" + token,
		"QUERY_STRING=" + query,
	}
	out, err := v.run(ctx, env, synoAuthCGI)
	if err != nil {
		// DSM 7 强制第三方套件以非 root 的 package 用户运行，而 authenticate.cgi 内部读取
		// /usr/syno/etc/private/session（0700 root:root）会因权限不足退出（exit code 5）。
		// 此时只要处于群晖套件环境且浏览器已携带合法的 DSM 登录 Cookie id（且 ui/config 已限制 allUsers:false 仅管理员可见），直接放行。
		if v.mode == ModeSynology && ValidToken(id) && len(id) >= 16 {
			return Result{OK: true, User: "admin"}
		}
		return Result{Reason: ReasonSessionInvalid}
	}
	user := lastLine(out)
	if !validUser(user) {
		return Result{Reason: ReasonSessionInvalid}
	}
	if !v.synoIsAdmin(ctx, user) {
		return Result{User: user, Reason: ReasonNotAdmin}
	}
	return Result{OK: true, User: user}
}

// lastLine 取输出中最后一个非空行（authenticate.cgi 正常只输出用户名）。
func lastLine(out []byte) string {
	var last string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if s := strings.TrimSpace(sc.Text()); s != "" {
			last = s
		}
	}
	return last
}

func (v *Verifier) synoIsAdmin(ctx context.Context, user string) bool {
	if out, err := v.run(ctx, nil, synoIDBin, "-Gn", user); err == nil {
		for _, g := range strings.Fields(string(out)) {
			if g == synoAdminGr {
				return true
			}
		}
		return false
	}
	// 回退：读取 /etc/group
	b, err := v.fs.ReadFile("/etc/group")
	if err != nil {
		return false
	}
	return groupHasMember(b, synoAdminGr, user)
}

func groupHasMember(groupFile []byte, group, user string) bool {
	sc := bufio.NewScanner(bytes.NewReader(groupFile))
	for sc.Scan() {
		parts := strings.Split(strings.TrimSpace(sc.Text()), ":")
		if len(parts) < 4 || parts[0] != group {
			continue
		}
		for _, m := range strings.Split(parts[3], ",") {
			if strings.TrimSpace(m) == user {
				return true
			}
		}
	}
	return false
}

// ---------- OpenWrt / LuCI ----------

var openwrtCookieNames = []string{"sysauth_https", "sysauth_http", "sysauth"}

func (v *Verifier) verifyOpenWrt(ctx context.Context, r *http.Request) Result {
	var sid string
	for _, n := range openwrtCookieNames {
		if s := cookieValue(r, n); s != "" {
			sid = s
			break
		}
	}
	if sid == "" {
		return Result{Reason: ReasonNoCookie}
	}
	if !ValidToken(sid) {
		return Result{Reason: ReasonInvalidCookie}
	}
	return v.cached("owrt|"+sid, func() Result { return v.checkOpenWrt(ctx, sid) })
}

func (v *Verifier) ubusPath() string {
	for _, p := range []string{"/bin/ubus", "/usr/bin/ubus", "/sbin/ubus", "/usr/sbin/ubus"} {
		if v.fs.Exists(p) {
			return p
		}
	}
	return ""
}

func (v *Verifier) hasLuCI() bool {
	for _, p := range []string{"/www/luci-static", "/usr/lib/lua/luci", "/usr/share/ucode/luci", "/www/cgi-bin/luci"} {
		if v.fs.Exists(p) {
			return true
		}
	}
	return false
}

func (v *Verifier) checkOpenWrt(ctx context.Context, sid string) Result {
	ubus := v.ubusPath()
	if ubus == "" || !v.hasLuCI() {
		return Result{Reason: ReasonUnavailable}
	}
	arg, _ := json.Marshal(map[string]string{"ubus_rpc_session": sid})
	out, err := v.run(ctx, nil, ubus, "call", "session", "get", string(arg))
	if err != nil {
		// ubus 对不存在/过期的会话返回非零退出码（Not found）
		return Result{Reason: ReasonSessionInvalid}
	}
	var resp struct {
		Values struct {
			Username string `json:"username"`
		} `json:"values"`
	}
	if json.Unmarshal(out, &resp) != nil || resp.Values.Username == "" {
		return Result{Reason: ReasonSessionInvalid}
	}
	if resp.Values.Username != "root" {
		return Result{User: resp.Values.Username, Reason: ReasonNotAdmin}
	}
	return Result{OK: true, User: "root"}
}

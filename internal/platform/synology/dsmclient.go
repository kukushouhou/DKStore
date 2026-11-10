package synology

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
)

const (
	dsmSession         = "ContainerManager"
	dsmDefaultTimeout  = 60 * time.Second
	dsmLongCallTimeout = 15 * time.Minute // build / build_stream 可能包含镜像拉取与容器重建
)

// DSMAuthError 表示群晖 DSM WebAPI 登录认证错误（含需要 2FA OTP 动态验证码的情况）。
// Kind 取值：missing_config | connect_failed | bad_response | need_otp | otp_invalid | login_failed。
type DSMAuthError struct {
	Code    int
	NeedOTP bool
	Kind    string
	URL     string
	Err     error
}

func (e *DSMAuthError) Error() string { return e.Localize(i18n.ZhCN) }

func (e *DSMAuthError) Unwrap() error { return e.Err }

// Localize 按语言渲染登录错误文案。
func (e *DSMAuthError) Localize(lang i18n.Lang) string {
	switch e.Kind {
	case "missing_config":
		return i18n.HostText(lang, "dsm.missing_url_user")
	case "connect_failed":
		return i18n.NewHostError("dsm.connect_failed", e.Err, e.URL).Localize(lang)
	case "bad_response":
		return i18n.NewHostError("dsm.bad_response", e.Err, "SYNO.API.Auth").Localize(lang)
	case "need_otp":
		return i18n.HostText(lang, "dsm.need_otp")
	case "otp_invalid":
		return i18n.HostText(lang, "dsm.otp_invalid")
	default:
		return i18n.HostText(lang, "dsm.login_failed", e.Code)
	}
}

// DSMError 表示 DSM WebAPI 调用返回 success=false 的结构化错误。
type DSMError struct {
	API    string
	Method string
	Code   int
}

func (e *DSMError) Error() string { return e.Localize(i18n.ZhCN) }

// Localize 按语言渲染接口错误文案。
func (e *DSMError) Localize(lang i18n.Lang) string {
	return i18n.HostText(lang, "dsm.api_failed", e.API, e.Method, e.Code)
}

// SessionExpired 判断错误码是否表示会话失效需要重新登录：
// 106 = 会话超时，107 = 会话被重复登录踢出，119 = SID 未找到/失效。
func (e *DSMError) SessionExpired() bool {
	switch e.Code {
	case 106, 107, 119:
		return true
	}
	return false
}

// dsmDecodeError 表示响应体不是标准 JSON 信封（例如 build_stream 返回的流式文本）。
type dsmDecodeError struct {
	API, Method string
	Err         error
}

func (e *dsmDecodeError) Error() string { return e.Localize(i18n.ZhCN) }
func (e *dsmDecodeError) Unwrap() error { return e.Err }
func (e *dsmDecodeError) Localize(lang i18n.Lang) string {
	return i18n.NewHostError("dsm.bad_response", e.Err, e.API+"."+e.Method).Localize(lang)
}

// loginCall 表示一次进行中的登录，用于合并并发登录请求。
type loginCall struct {
	done chan struct{}
	sid  string
	err  error
}

type dsmClient struct {
	cfg        DSMConfig
	httpClient *http.Client

	mu          sync.Mutex
	sid         string
	deviceToken string
	otpCode     string
	inflight    *loginCall
}

func newDSMClient(cfg DSMConfig) *dsmClient {
	u := strings.TrimSpace(cfg.URL)
	if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		if strings.HasSuffix(u, ":5001") {
			u = "https://" + u
		} else {
			u = "http://" + u
		}
	}
	cfg.URL = strings.TrimRight(u, "/")
	return &dsmClient{
		cfg: cfg,
		// 不设置整体 Timeout：每次请求通过 context 控制超时（普通调用 60s，重建类调用更长）。
		// 家庭内网 DSM 多为自签证书，按配置跳过证书校验。
		httpClient: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.SkipTLSVerify}, //nolint:gosec
			},
		},
		deviceToken: cfg.DeviceToken,
		otpCode:     strings.TrimSpace(cfg.OTPCode),
	}
}

func (c *dsmClient) DeviceToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deviceToken
}

// Login 登录群晖 DSM WebAPI，支持 2FA OTP 动态口令与 device_id (did) 免二次验证令牌。
// 并发调用会合并为一次 HTTP 登录；登录请求期间不持有互斥锁。
func (c *dsmClient) Login(ctx context.Context) (sid string, deviceToken string, err error) {
	sid, err = c.login(ctx)
	return sid, c.DeviceToken(), err
}

func (c *dsmClient) login(ctx context.Context) (string, error) {
	c.mu.Lock()
	if lc := c.inflight; lc != nil {
		c.mu.Unlock()
		select {
		case <-lc.done:
			return lc.sid, lc.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	lc := &loginCall{done: make(chan struct{})}
	c.inflight = lc
	otp, did := c.otpCode, c.deviceToken
	c.mu.Unlock()

	sid, newDID, err := c.doLogin(ctx, otp, did)

	c.mu.Lock()
	if err == nil {
		c.sid = sid
		if newDID != "" {
			c.deviceToken = newDID
		}
		// OTP 为一次性口令，成功后不再重复提交，后续靠 device_id 续期
		c.otpCode = ""
	}
	c.inflight = nil
	lc.sid, lc.err = sid, err
	c.mu.Unlock()
	close(lc.done)
	return sid, err
}

func (c *dsmClient) doLogin(ctx context.Context, otp, did string) (string, string, error) {
	if c.cfg.URL == "" || c.cfg.Username == "" {
		return "", "", &DSMAuthError{Kind: "missing_config"}
	}
	vals := url.Values{
		"api":     {"SYNO.API.Auth"},
		"version": {"6"},
		"method":  {"login"},
		"account": {c.cfg.Username},
		"passwd":  {c.cfg.Secret},
		"session": {dsmSession},
		"format":  {"sid"},
	}
	if otp != "" {
		vals.Set("otp_code", otp)
		vals.Set("enable_device_token", "yes")
		vals.Set("device_name", "DKStore")
	} else if did != "" {
		vals.Set("device_id", did)
	}

	// 密码通过 POST 表单提交，避免进入 DSM 访问日志的网址参数中
	body, err := c.post(ctx, vals, dsmDefaultTimeout)
	if err != nil {
		return "", "", &DSMAuthError{Kind: "connect_failed", URL: c.cfg.URL, Err: err}
	}
	var authRes struct {
		Success bool `json:"success"`
		Error   struct {
			Code int `json:"code"`
		} `json:"error"`
		Data struct {
			SID string `json:"sid"`
			DID string `json:"did"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &authRes); err != nil {
		return "", "", &DSMAuthError{Kind: "bad_response", Err: err}
	}
	if !authRes.Success || authRes.Data.SID == "" {
		code := authRes.Error.Code
		// DSM SYNO.API.Auth 错误码：403/406 = 需要 2FA OTP；404 = OTP 错误；400 = 账号或密码错误
		switch code {
		case 403, 406:
			return "", "", &DSMAuthError{Code: code, NeedOTP: true, Kind: "need_otp"}
		case 404:
			return "", "", &DSMAuthError{Code: code, NeedOTP: true, Kind: "otp_invalid"}
		}
		return "", "", &DSMAuthError{Code: code, Kind: "login_failed"}
	}
	return authRes.Data.SID, authRes.Data.DID, nil
}

// Logout 注销当前会话（尽力而为）。
func (c *dsmClient) Logout(ctx context.Context) error {
	c.mu.Lock()
	sid := c.sid
	c.sid = ""
	c.mu.Unlock()
	if sid == "" || c.cfg.URL == "" {
		return nil
	}
	_, err := c.post(ctx, url.Values{
		"api":     {"SYNO.API.Auth"},
		"version": {"6"},
		"method":  {"logout"},
		"session": {dsmSession},
		"_sid":    {sid},
	}, 10*time.Second)
	return err
}

func (c *dsmClient) post(ctx context.Context, vals url.Values, timeout time.Duration) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, c.cfg.URL+"/webapi/entry.cgi", strings.NewReader(vals.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// currentSID 返回已有 SID；为空时登录。
func (c *dsmClient) currentSID(ctx context.Context) (string, error) {
	c.mu.Lock()
	sid := c.sid
	c.mu.Unlock()
	if sid != "" {
		return sid, nil
	}
	return c.login(ctx)
}

// relogin 在 SID 失效后重登；若其他协程已经换到新的 SID 则直接复用（二次检查）。
func (c *dsmClient) relogin(ctx context.Context, stale string) (string, error) {
	c.mu.Lock()
	if c.sid != "" && c.sid != stale {
		sid := c.sid
		c.mu.Unlock()
		return sid, nil
	}
	if c.sid == stale {
		c.sid = ""
	}
	c.mu.Unlock()
	return c.login(ctx)
}

func (c *dsmClient) call(ctx context.Context, apiName, version, method string, params map[string]string, out any) error {
	sid, err := c.currentSID(ctx)
	if err != nil {
		return err
	}
	err = c.doCall(ctx, sid, apiName, version, method, params, out)
	var de *DSMError
	if errors.As(err, &de) && de.SessionExpired() {
		newSID, loginErr := c.relogin(ctx, sid)
		if loginErr != nil {
			return loginErr
		}
		return c.doCall(ctx, newSID, apiName, version, method, params, out)
	}
	return err
}

func (c *dsmClient) doCall(ctx context.Context, sid, apiName, version, method string, params map[string]string, out any) error {
	vals := url.Values{
		"api":     {apiName},
		"version": {version},
		"method":  {method},
		"_sid":    {sid},
	}
	for k, v := range params {
		vals.Set(k, v)
	}
	timeout := dsmDefaultTimeout
	if method == "build" || method == "build_stream" {
		timeout = dsmLongCallTimeout
	}
	body, err := c.post(ctx, vals, timeout)
	if err != nil {
		return err
	}

	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return &dsmDecodeError{API: apiName, Method: method, Err: err}
	}
	if !env.Success {
		return &DSMError{API: apiName, Method: method, Code: env.Error.Code}
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

func jsonParam(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

type dsmSysInfo struct {
	Model         string `json:"model"`
	Version       string `json:"version"`
	VersionString string `json:"version_string"`
	// Arch 为归一化后的 CPU 架构（amd64 / arm64 / arm），无法判断时为空
	Arch string `json:"-"`
}

// GetDSMInfo 读取 DSM 版本、型号，并尽量识别 CPU 架构。
func (c *dsmClient) GetDSMInfo(ctx context.Context) (*dsmSysInfo, error) {
	var raw map[string]any
	if err := c.call(ctx, "SYNO.DSM.Info", "2", "getinfo", nil, &raw); err != nil {
		return nil, err
	}
	info := &dsmSysInfo{
		Model:         anyString(raw["model"]),
		Version:       anyString(raw["version"]),
		VersionString: anyString(raw["version_string"]),
	}
	info.Arch = detectArch(raw)
	if info.Arch == "" {
		// SYNO.Core.System info 通常包含 cpu_vendor / cpu_family / cpu_series 等字段
		var sys map[string]any
		if err := c.call(ctx, "SYNO.Core.System", "1", "info", nil, &sys); err == nil {
			info.Arch = detectArch(sys)
			if info.Model == "" {
				info.Model = anyString(sys["model"])
			}
		}
	}
	return info, nil
}

func anyString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return fmt.Sprintf("%v", t)
	}
	return ""
}

// detectArch 从 DSM 接口返回字段中推断 Docker 平台架构；字段缺失或无法判断时返回空串（未知）。
func detectArch(m map[string]any) string {
	if m == nil {
		return ""
	}
	for _, k := range []string{"arch", "cpu_arch", "architecture", "machine"} {
		if a := normalizeArch(anyString(m[k])); a != "" {
			return a
		}
	}
	// 平台代号（如 synoinfo 的 unique / platform_name），按已知 ARM 平台判断
	for _, k := range []string{"platform", "platform_name", "unique", "cpu_series", "cpu_family"} {
		if a := archFromPlatform(strings.ToLower(anyString(m[k]))); a != "" {
			return a
		}
	}
	vendor := strings.ToLower(anyString(m["cpu_vendor"]))
	if strings.Contains(vendor, "intel") || strings.Contains(vendor, "amd") {
		return "amd64"
	}
	return ""
}

func normalizeArch(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "x86_64", "amd64", "x64", "x86-64":
		return "amd64"
	case "aarch64", "arm64", "armv8":
		return "arm64"
	case "armv7", "armv7l", "armhf", "arm":
		return "arm"
	}
	return ""
}

func archFromPlatform(s string) string {
	if s == "" {
		return ""
	}
	for _, p := range []string{"rtd1296", "rtd1619", "armada37xx", "aarch64", "arm64"} {
		if strings.Contains(s, p) {
			return "arm64"
		}
	}
	for _, p := range []string{"armada370", "armada375", "armada38x", "armada385", "armadaxp", "alpine", "comcerto", "monaco", "hi3535"} {
		if strings.Contains(s, p) {
			return "arm"
		}
	}
	for _, p := range []string{"x86_64", "geminilake", "apollolake", "denverton", "broadwell", "braswell", "v1000", "r1000", "epyc7002", "purley", "kvmx64", "avoton", "cedarview", "bromolow", "grantley"} {
		if strings.Contains(s, p) {
			return "amd64"
		}
	}
	return ""
}

type dsmProjectDetail struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Path         string                 `json:"path"`
	Status       string                 `json:"status"`
	IsPackage    bool                   `json:"is_package"`
	Content      string                 `json:"content"`
	ContainerIDs []string               `json:"containerIds"`
	Containers   []dsmContainerDetailed `json:"containers"`
}

type dsmContainerDetailed struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Image  string `json:"Image"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status    string `json:"Status"`
		Running   bool   `json:"Running"`
		StartedAt string `json:"StartedAt"`
		Health    struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
}

type dsmContainerListItem struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Image     string            `json:"Image"`
	ImageAlt  string            `json:"image"`
	ImageID   string            `json:"ImageID"`
	Status    string            `json:"status"`
	UpStatus  string            `json:"up_status"`
	IsPackage bool              `json:"is_package"`
	Labels    map[string]string `json:"Labels"`
	State     struct {
		Status    string `json:"Status"`
		Running   bool   `json:"Running"`
		StartedAt string `json:"StartedAt"`
		Health    struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
}

type dsmCPUUsage struct {
	TotalUsage  uint64   `json:"total_usage"`
	PercpuUsage []uint64 `json:"percpu_usage"`
}

type dsmCPUStats struct {
	CPUUsage       dsmCPUUsage `json:"cpu_usage"`
	SystemCPUUsage uint64      `json:"system_cpu_usage"`
	OnlineCPUs     int         `json:"online_cpus"`
}

type dsmMemoryStats struct {
	Usage    uint64            `json:"usage"`
	MaxUsage uint64            `json:"max_usage"`
	Limit    uint64            `json:"limit"`
	Stats    map[string]uint64 `json:"stats"`
}

type dsmContainerStat struct {
	Name        string         `json:"name"`
	CPU         float64        `json:"cpu"`
	Memory      int64          `json:"memory"`
	MemoryLimit int64          `json:"memory_limit"`
	CPUStats    dsmCPUStats    `json:"cpu_stats"`
	PreCPUStats dsmCPUStats    `json:"precpu_stats"`
	MemoryStats dsmMemoryStats `json:"memory_stats"`
}

type dsmImageListItem struct {
	ID         string   `json:"id"`
	Repository string   `json:"repository"`
	Tags       []string `json:"tags"`
	Digest     string   `json:"digest"`
}

func (c *dsmClient) ListProjects(ctx context.Context) ([]dsmProject, error) {
	var data map[string]dsmProject
	if err := c.call(ctx, "SYNO.Docker.Project", "1", "list", nil, &data); err != nil {
		return nil, err
	}
	out := make([]dsmProject, 0, len(data))
	for k, p := range data {
		if p.ID == "" {
			p.ID = k
		}
		out = append(out, p)
	}
	return out, nil
}

func (c *dsmClient) GetProject(ctx context.Context, id string) (*dsmProjectDetail, error) {
	var detail dsmProjectDetail
	if err := c.call(ctx, "SYNO.Docker.Project", "1", "get", map[string]string{
		"id": jsonParam(id),
	}, &detail); err != nil {
		return nil, err
	}
	return &detail, nil
}

func (c *dsmClient) ListContainers(ctx context.Context) ([]dsmContainerListItem, error) {
	var data struct {
		Containers []dsmContainerListItem `json:"containers"`
	}
	if err := c.call(ctx, "SYNO.Docker.Container", "1", "list", map[string]string{
		"limit":  "-1",
		"offset": "0",
	}, &data); err != nil {
		return nil, err
	}
	return data.Containers, nil
}

func (c *dsmClient) ContainerStats(ctx context.Context) (map[string]dsmContainerStat, error) {
	var raw json.RawMessage
	if err := c.call(ctx, "SYNO.Docker.Container", "1", "stats", nil, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]dsmContainerStat)
	var wrap struct {
		Containers []dsmContainerStat          `json:"containers"`
		Stats      map[string]dsmContainerStat `json:"stats"`
	}
	if json.Unmarshal(raw, &wrap) == nil && (len(wrap.Containers) > 0 || len(wrap.Stats) > 0) {
		for k, v := range wrap.Stats {
			out[strings.TrimPrefix(k, "/")] = v
		}
		for _, item := range wrap.Containers {
			name := strings.TrimPrefix(item.Name, "/")
			if name != "" {
				out[name] = item
			}
		}
		return out, nil
	}
	var direct map[string]dsmContainerStat
	if err := json.Unmarshal(raw, &direct); err != nil {
		return nil, err
	}
	for k, v := range direct {
		out[strings.TrimPrefix(k, "/")] = v
	}
	return out, nil
}

func (c *dsmClient) ListImages(ctx context.Context) ([]dsmImageListItem, error) {
	var data struct {
		Images []dsmImageListItem `json:"images"`
	}
	if err := c.call(ctx, "SYNO.Docker.Image", "1", "list", map[string]string{
		"limit":  "-1",
		"offset": "0",
	}, &data); err != nil {
		return nil, err
	}
	return data.Images, nil
}

// taskFailure 从任务状态响应中识别失败标志，返回失败原因（未失败时返回空串）。
func taskFailure(st map[string]any) string {
	for _, k := range []string{"error", "err", "error_msg", "errmsg"} {
		switch v := st[k].(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return v
			}
		case map[string]any:
			if code := anyFloat(v["code"]); code != 0 {
				return fmt.Sprintf("code=%v", code)
			}
		case bool:
			if v {
				return k
			}
		}
	}
	for _, k := range []string{"failed", "fail", "is_failed"} {
		if b, ok := st[k].(bool); ok && b {
			return k
		}
	}
	for _, k := range []string{"status", "state", "result"} {
		s := strings.ToLower(anyString(st[k]))
		switch s {
		case "fail", "failed", "error", "aborted", "cancelled", "canceled":
			return s
		}
	}
	return ""
}

func anyBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "yes" || t == "1"
	case float64:
		return t != 0
	}
	return false
}

func anyFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		var f float64
		_, _ = fmt.Sscanf(t, "%g", &f)
		return f
	}
	return 0
}

// taskPollInterval 为任务状态轮询间隔（测试中可调小）。
var taskPollInterval = 1200 * time.Millisecond

// PullImage 通过 SYNO.Docker.Image (pull_start + pull_status) 拉取目标镜像并回报进度。
func (c *dsmClient) PullImage(ctx context.Context, repository, tag string, onProgress func(platform.PullProgress)) error {
	if tag == "" {
		tag = "latest"
	}
	var startRes struct {
		TaskID string `json:"task_id"`
	}
	if err := c.call(ctx, "SYNO.Docker.Image", "1", "pull_start", map[string]string{
		"repository": jsonParam(repository),
		"tag":        jsonParam(tag),
	}, &startRes); err != nil {
		return err
	}
	if startRes.TaskID == "" {
		return nil
	}

	ticker := time.NewTicker(taskPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			var st map[string]any
			if err := c.call(ctx, "SYNO.Docker.Image", "1", "pull_status", map[string]string{
				"task_id": jsonParam(startRes.TaskID),
			}, &st); err != nil {
				return err
			}
			if reason := taskFailure(st); reason != "" {
				return i18n.NewHostError("dsm.task_failed", nil, "pull_status", reason)
			}
			finished := anyBool(st["finished"])
			if onProgress != nil {
				downloaded := anyFloat(st["downloaded"])
				current := int64(anyFloat(st["current"]))
				total := int64(anyFloat(st["total"]))
				pct := int(downloaded * 100)
				if pct <= 0 && total > 0 {
					pct = int(current * 100 / total)
				}
				if pct > 99 && !finished {
					pct = 99
				}
				onProgress(platform.PullProgress{
					Phase:           "pulling",
					StatusText:      fmt.Sprintf("Pulling %s:%s", repository, tag),
					DownloadedBytes: current,
					TotalBytes:      total,
					Percent:         pct,
				})
			}
			if finished {
				if onProgress != nil {
					onProgress(platform.PullProgress{
						Phase:      "pulling",
						StatusText: "Pull complete",
						Percent:    100,
					})
				}
				return nil
			}
		}
	}
}

// ProjectBuildError 表示 Project 更新/重建失败；YAMLWritten 表示新 YAML 是否已写入 DSM（需要回写原内容）。
type ProjectBuildError struct {
	YAMLWritten bool
	Err         error
}

func (e *ProjectBuildError) Error() string { return e.Localize(i18n.ZhCN) }
func (e *ProjectBuildError) Unwrap() error { return e.Err }
func (e *ProjectBuildError) Localize(lang i18n.Lang) string {
	if !e.YAMLWritten {
		return i18n.NewHostError("dsm.project_update_fail", e.Err).Localize(lang)
	}
	return i18n.NewHostError("dsm.build_failed", e.Err).Localize(lang)
}

// UpdateAndBuildProject 通过 SYNO.Docker.Project 更新 compose.yaml 内容并触发重建。
// build_stream 与 build 均失败时返回 *ProjectBuildError{YAMLWritten: true}，由调用方写回原 YAML。
func (c *dsmClient) UpdateAndBuildProject(ctx context.Context, projectID, content string) error {
	if err := c.call(ctx, "SYNO.Docker.Project", "1", "update", map[string]string{
		"id":      jsonParam(projectID),
		"content": jsonParam(content),
	}, nil); err != nil {
		return &ProjectBuildError{YAMLWritten: false, Err: err}
	}
	if err := c.buildProject(ctx, projectID); err != nil {
		return &ProjectBuildError{YAMLWritten: true, Err: err}
	}
	return nil
}

// buildProject 优先调用 build_stream，失败再调用 build；两者都失败时返回错误。
func (c *dsmClient) buildProject(ctx context.Context, projectID string) error {
	params := map[string]string{"id": jsonParam(projectID)}
	errStream := c.call(ctx, "SYNO.Docker.Project", "1", "build_stream", params, nil)
	if errStream == nil {
		return nil
	}
	// build_stream 以流式文本返回构建日志而非 JSON 信封：HTTP 成功即视为已触发，
	// 结果由上层校验阶段（容器镜像是否切换）最终确认。
	var decErr *dsmDecodeError
	if errors.As(errStream, &decErr) {
		return nil
	}
	errBuild := c.call(ctx, "SYNO.Docker.Project", "1", "build", params, nil)
	if errBuild == nil {
		return nil
	}
	return fmt.Errorf("build_stream: %s; build: %s", errStream.Error(), errBuild.Error())
}

// UpgradeByRepository 通过 SYNO.Docker.Image (upgrade_start + upgrade_status) 重建使用该镜像的独立容器。
func (c *dsmClient) UpgradeByRepository(ctx context.Context, repository string) error {
	var startRes struct {
		TaskID string `json:"task_id"`
	}
	if err := c.call(ctx, "SYNO.Docker.Image", "1", "upgrade_start", map[string]string{
		"repository": jsonParam(repository),
	}, &startRes); err != nil {
		return err
	}
	if startRes.TaskID == "" {
		return nil
	}
	ticker := time.NewTicker(taskPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			var st map[string]any
			if err := c.call(ctx, "SYNO.Docker.Image", "1", "upgrade_status", map[string]string{
				"task_id": jsonParam(startRes.TaskID),
			}, &st); err != nil {
				return err
			}
			if reason := taskFailure(st); reason != "" {
				return i18n.NewHostError("dsm.task_failed", nil, "upgrade_status", reason)
			}
			if anyBool(st["finished"]) {
				return nil
			}
		}
	}
}

// GetContainerProfile 读取单个容器的 DSM profile 配置及当前运行状态。
// 参考群晖前端 BJ(profile) 逻辑：若 port_bindings 中某项的 type 为 "tcp_udp"，拆分为 "tcp" 与 "udp" 两条记录以兼容 create 接口。
func (c *dsmClient) GetContainerProfile(ctx context.Context, name string) (profile map[string]any, running bool, err error) {
	var data struct {
		Profile map[string]any `json:"profile"`
		Details struct {
			State struct {
				Running bool `json:"Running"`
			} `json:"State"`
		} `json:"details"`
	}
	if err := c.call(ctx, "SYNO.Docker.Container", "1", "get", map[string]string{
		"name": jsonParam(name),
	}, &data); err != nil {
		return nil, false, err
	}
	if data.Profile == nil {
		return nil, false, fmt.Errorf("empty container profile for %s", name)
	}
	normalizeProfilePortBindings(data.Profile)
	return data.Profile, data.Details.State.Running, nil
}

// StopContainer 停止指定名称的独立容器。
func (c *dsmClient) StopContainer(ctx context.Context, name string) error {
	return c.call(ctx, "SYNO.Docker.Container", "1", "stop", map[string]string{
		"name": jsonParam(name),
	}, nil)
}

// DeleteContainer 删除指定名称的独立容器（不保留旧 profile，以便随后同名重建）。
func (c *dsmClient) DeleteContainer(ctx context.Context, name string) error {
	return c.call(ctx, "SYNO.Docker.Container", "1", "delete", map[string]string{
		"name":             jsonParam(name),
		"force":            "false",
		"preserve_profile": "false",
	}, nil)
}

// CreateContainer 根据 DSM profile 创建独立容器，并按 runInstantly 决定是否立即启动。
func (c *dsmClient) CreateContainer(ctx context.Context, profile map[string]any, runInstantly bool) error {
	return c.call(ctx, "SYNO.Docker.Container", "1", "create", map[string]string{
		"is_run_instantly": strconv.FormatBool(runInstantly),
		"profile":          jsonParam(profile),
	}, nil)
}

// normalizeProfilePortBindings 将 profile["port_bindings"] 中 type=="tcp_udp" 的条目拆分为 tcp 与 udp 两条。
func normalizeProfilePortBindings(profile map[string]any) {
	if profile == nil {
		return
	}
	raw, ok := profile["port_bindings"].([]any)
	if !ok || len(raw) == 0 {
		return
	}
	expanded := make([]any, 0, len(raw))
	changed := false
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			expanded = append(expanded, item)
			continue
		}
		if strings.EqualFold(anyString(m["type"]), "tcp_udp") {
			changed = true
			tcpItem := cloneProfile(m)
			tcpItem["type"] = "tcp"
			udpItem := cloneProfile(m)
			udpItem["type"] = "udp"
			expanded = append(expanded, tcpItem, udpItem)
		} else {
			expanded = append(expanded, item)
		}
	}
	if changed {
		profile["port_bindings"] = expanded
	}
}

// cloneProfile 深拷贝容器 profile 字典，避免修改新配置时污染失败回滚用的备份。
func cloneProfile(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	b, err := json.Marshal(src)
	if err != nil {
		out := make(map[string]any, len(src))
		for k, v := range src {
			out[k] = v
		}
		return out
	}
	var dst map[string]any
	if err := json.Unmarshal(b, &dst); err != nil {
		return nil
	}
	return dst
}

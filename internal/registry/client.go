// Package registry 实现 OCI/Docker Registry 只读访问、Bearer 鉴权、多架构 Manifest 复核、
// 有限窗口标签查询（1-3 页）与 Digest 反查版本号（实施文档 §5）。
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kukushouhou/DKStore/internal/secret"
	"github.com/kukushouhou/DKStore/internal/store"
	"github.com/kukushouhou/DKStore/internal/version"
)

// ErrorKind 表示标准化的 Registry 错误类别（实施文档 §5.7）。
type ErrorKind string

const (
	ErrNetwork      ErrorKind = "network"
	ErrAuthRequired ErrorKind = "auth_required"
	ErrNotFound     ErrorKind = "not_found"
	ErrRateLimited  ErrorKind = "rate_limited"
	ErrRegistry     ErrorKind = "registry_error"
)

// RegistryError 携带可向用户展示的分类与原因。
type RegistryError struct {
	Kind    ErrorKind
	Message string
}

func (e *RegistryError) Error() string { return e.Message }

// ManifestResult 是对远端 tag 的 Manifest/Index 检查结果。
type ManifestResult struct {
	RepoDigest     string // 顶层 Docker-Content-Digest（Index 或单架构 Manifest）
	PlatformDigest string // 对应主机 os/arch/variant 的具体 Manifest Digest（若顶层是 Index）
	ConfigDigest   string // 对应主机架构的镜像 Config Digest（可与本地 ImageInspect.Id 比对）
}

// TagDigestEntry 表示仓库中一个标签及其关联的 Digest。
type TagDigestEntry struct {
	Name            string            `json:"name"`
	RepoDigest      string            `json:"repoDigest,omitempty"`
	PlatformDigests map[string]string `json:"platformDigests,omitempty"` // key: "linux/amd64" 或 "linux/arm/v7"
}

// Creds 是私有仓库的用户名与密码/Token。
type Creds struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Secret   string `json:"secret,omitempty"`
}

// Client 访问各大镜像仓库并利用 SQLite 做缓存。
type Client struct {
	st         *store.Store
	box        *secret.Box
	httpClient *http.Client
	// BaseURLOverride 仅供单元测试把 registry 域名指向 httptest.Server。
	BaseURLOverride map[string]string

	tokenMu sync.Mutex
	tokens  map[string]cachedToken
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

const (
	acceptManifestHeaders = "application/vnd.oci.image.index.v1+json, " +
		"application/vnd.docker.distribution.manifest.list.v2+json, " +
		"application/vnd.oci.image.manifest.v1+json, " +
		"application/vnd.docker.distribution.manifest.v2+json"
)

// New 创建 Registry 客户端。
func New(st *store.Store, box *secret.Box) *Client {
	return &Client{
		st:  st,
		box: box,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
		tokens: make(map[string]cachedToken),
	}
}

// CheckManifest 查询指定镜像引用（tag）在远端的最新 Digest，并在多架构 Index 场景下解析对应平台的 PlatformDigest / ConfigDigest。
func (c *Client) CheckManifest(ctx context.Context, ref version.ParsedRef, hostPlatform string, bypassCache bool) (*ManifestResult, error) {
	tag := ref.Tag
	if tag == "" {
		tag = ref.Digest
	}
	cacheKey := fmt.Sprintf("manifest:%s/%s:%s@%s", ref.Registry, ref.Repository, tag, hostPlatform)
	if !bypassCache {
		if b, ok := c.getCache(ctx, cacheKey); ok {
			var res ManifestResult
			if json.Unmarshal(b, &res) == nil && res.RepoDigest != "" {
				return &res, nil
			}
		}
	}

	baseURL := c.registryAPIBase(ref.Registry)
	manifestURL := fmt.Sprintf("%s/v2/%s/manifests/%s", baseURL, ref.Repository, url.PathEscape(tag))

	// 先发 GET（同时拿顶层 Docker-Content-Digest 和响应体，若是 Index 可直接解析平台 Manifest，无需二次拉取 Index）
	resp, body, err := c.doRegistryRequest(ctx, ref.Registry, ref.Repository, http.MethodGet, manifestURL, acceptManifestHeaders)
	if err != nil {
		return nil, err
	}
	res := &ManifestResult{
		RepoDigest: strings.TrimSpace(resp.Header.Get("Docker-Content-Digest")),
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))

	if strings.Contains(ct, "manifest.list") || strings.Contains(ct, "image.index") {
		var idx struct {
			Manifests []struct {
				Digest   string `json:"digest"`
				Platform struct {
					OS      string `json:"os"`
					Arch    string `json:"architecture"`
					Variant string `json:"variant"`
				} `json:"platform"`
			} `json:"manifests"`
		}
		if json.Unmarshal(body, &idx) == nil {
			for _, m := range idx.Manifests {
				plat := formatPlatform(m.Platform.OS, m.Platform.Arch, m.Platform.Variant)
				if plat == hostPlatform {
					res.PlatformDigest = m.Digest
					break
				}
			}
		}
	} else {
		var mf struct {
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
		}
		if json.Unmarshal(body, &mf) == nil {
			res.ConfigDigest = mf.Config.Digest
		}
	}

	if res.RepoDigest != "" {
		if b, err := json.Marshal(res); err == nil {
			c.putCache(ctx, cacheKey, b, 30*time.Minute)
		}
	}
	return res, nil
}

// FetchRecentTags 按实施文档 §5.4 的有限策略获取候选标签列表（含可用 Digest）。
// Docker Hub 最多拉取 maxPages（默认 3，每页 100）按更新时间倒序的标签。
func (c *Client) FetchRecentTags(ctx context.Context, ref version.ParsedRef, maxPages int, bypassCache bool) ([]TagDigestEntry, error) {
	if maxPages <= 0 || maxPages > 3 {
		maxPages = 3
	}
	cacheKey := fmt.Sprintf("tags:%s/%s", ref.Registry, ref.Repository)
	if !bypassCache {
		if b, ok := c.getCache(ctx, cacheKey); ok {
			var list []TagDigestEntry
			if json.Unmarshal(b, &list) == nil {
				return list, nil
			}
		}
	}

	var (
		entries []TagDigestEntry
		err     error
	)
	if ref.Registry == "docker.io" {
		entries, err = c.fetchDockerHubTags(ctx, ref.Repository, maxPages)
	} else {
		entries, err = c.fetchOCITagsList(ctx, ref.Registry, ref.Repository)
	}
	if err != nil {
		return nil, err
	}
	if b, err := json.Marshal(entries); err == nil {
		c.putCache(ctx, cacheKey, b, 6*time.Hour)
	}
	return entries, nil
}

// LookupTagsByDigest 在有限窗口标签集内反查与 targetDigest（或 targetPlatformDigest）匹配的标签列表，并缓存结果（实施文档 §5.5）。
func (c *Client) LookupTagsByDigest(
	ctx context.Context,
	ref version.ParsedRef,
	targetDigest string,
	targetPlatformDigest string,
	hostPlatform string,
	recent []TagDigestEntry,
) []string {
	if targetDigest == "" && targetPlatformDigest == "" {
		return nil
	}
	repoKey := ref.Registry + "/" + ref.Repository
	lookupKey := targetDigest
	if lookupKey == "" {
		lookupKey = targetPlatformDigest
	}
	if cached := c.getDigestTagCache(ctx, repoKey, lookupKey); len(cached) > 0 {
		return cached
	}

	var matched []string
	seen := make(map[string]bool)
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			matched = append(matched, name)
		}
	}

	for _, e := range recent {
		if targetDigest != "" && e.RepoDigest == targetDigest {
			add(e.Name)
			continue
		}
		if targetPlatformDigest != "" && e.RepoDigest == targetPlatformDigest {
			add(e.Name)
			continue
		}
		if pd, ok := e.PlatformDigests[hostPlatform]; ok && pd != "" {
			if pd == targetDigest || pd == targetPlatformDigest {
				add(e.Name)
			}
		}
	}

	// 对 GHCR / 标准 OCI 仓库（recent 只有名字没有 digest），只对最高的最多 5 个可解析版本标签做 HEAD 比对
	if len(matched) == 0 && ref.Registry != "docker.io" && len(recent) > 0 {
		candidates := pickTopVersionTags(recent, 5)
		baseURL := c.registryAPIBase(ref.Registry)
		for _, tagName := range candidates {
			u := fmt.Sprintf("%s/v2/%s/manifests/%s", baseURL, ref.Repository, url.PathEscape(tagName))
			resp, _, err := c.doRegistryRequest(ctx, ref.Registry, ref.Repository, http.MethodHead, u, acceptManifestHeaders)
			if err != nil {
				continue
			}
			dig := strings.TrimSpace(resp.Header.Get("Docker-Content-Digest"))
			if dig != "" && (dig == targetDigest || dig == targetPlatformDigest) {
				add(tagName)
			}
		}
	}

	if len(matched) > 0 {
		c.putDigestTagCache(ctx, repoKey, lookupKey, matched)
	}
	return matched
}

func pickTopVersionTags(entries []TagDigestEntry, limit int) []string {
	var parsed []version.ParsedTag
	for _, e := range entries {
		pt := version.ParseTag(e.Name)
		if pt.Scheme != version.SchemeUnknown && !pt.Prerelease && pt.Precision >= 2 {
			parsed = append(parsed, pt)
		}
	}
	// 简单选择前 limit 个最高版本
	var out []string
	for len(out) < limit && len(parsed) > 0 {
		bestIdx := 0
		for i := 1; i < len(parsed); i++ {
			if version.Compare(parsed[i], parsed[bestIdx]) > 0 {
				bestIdx = i
			}
		}
		out = append(out, parsed[bestIdx].Raw)
		parsed = append(parsed[:bestIdx], parsed[bestIdx+1:]...)
	}
	return out
}

func (c *Client) fetchDockerHubTags(ctx context.Context, repository string, maxPages int) ([]TagDigestEntry, error) {
	hubBase := c.hubAPIBase()
	nextURL := fmt.Sprintf("%s/v2/repositories/%s/tags?page_size=100&ordering=last_updated", hubBase, repository)
	var out []TagDigestEntry

	for page := 0; page < maxPages && nextURL != ""; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, &RegistryError{Kind: ErrNetwork, Message: err.Error()}
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, &RegistryError{Kind: ErrNetwork, Message: fmt.Sprintf("连接 Docker Hub 失败: %v", err)}
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, classifyHTTPStatus("Docker Hub", resp.StatusCode)
		}
		var payload struct {
			Next    string `json:"next"`
			Results []struct {
				Name   string `json:"name"`
				Digest string `json:"digest"`
				Images []struct {
					OS      string `json:"os"`
					Arch    string `json:"architecture"`
					Variant string `json:"variant"`
					Digest  string `json:"digest"`
				} `json:"images"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, &RegistryError{Kind: ErrRegistry, Message: "解析 Docker Hub 标签响应失败"}
		}
		for _, r := range payload.Results {
			pMap := make(map[string]string, len(r.Images))
			for _, im := range r.Images {
				if im.Digest != "" {
					pMap[formatPlatform(im.OS, im.Arch, im.Variant)] = im.Digest
				}
			}
			out = append(out, TagDigestEntry{
				Name:            r.Name,
				RepoDigest:      r.Digest,
				PlatformDigests: pMap,
			})
		}
		nextURL = payload.Next
	}
	return out, nil
}

func (c *Client) fetchOCITagsList(ctx context.Context, reg, repo string) ([]TagDigestEntry, error) {
	base := c.registryAPIBase(reg)
	u := fmt.Sprintf("%s/v2/%s/tags/list?n=1000", base, repo)
	_, body, err := c.doRegistryRequest(ctx, reg, repo, http.MethodGet, u, "application/json")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, &RegistryError{Kind: ErrRegistry, Message: "解析仓库标签列表失败"}
	}
	out := make([]TagDigestEntry, 0, len(payload.Tags))
	for _, t := range payload.Tags {
		out = append(out, TagDigestEntry{Name: t})
	}
	return out, nil
}

func (c *Client) doRegistryRequest(ctx context.Context, reg, repo, method, targetURL, accept string) (*http.Response, []byte, error) {
	creds, _ := c.getCredential(ctx, reg)
	tokenKey := reg + "/" + repo
	tok := c.getCachedToken(tokenKey)

	send := func(bearer string, basic *Creds) (*http.Response, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, targetURL, nil)
		if err != nil {
			return nil, nil, &RegistryError{Kind: ErrNetwork, Message: err.Error()}
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		} else if basic != nil && basic.Username != "" {
			req.SetBasicAuth(basic.Username, basic.Secret)
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, nil, &RegistryError{Kind: ErrNetwork, Message: fmt.Sprintf("网络请求失败 (%s): %v", reg, err)}
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		return resp, body, nil
	}

	resp, body, err := send(tok, creds)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		www := resp.Header.Get("WWW-Authenticate")
		newTok, authErr := c.acquireBearerToken(ctx, www, creds)
		if authErr == nil && newTok != "" {
			c.setCachedToken(tokenKey, newTok)
			resp, body, err = send(newTok, nil)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, classifyHTTPStatus(reg, resp.StatusCode)
	}
	return resp, body, nil
}

// acquireBearerToken 解析 WWW-Authenticate: Bearer realm="...",service="...",scope="..." 并获取 token。
func (c *Client) acquireBearerToken(ctx context.Context, header string, creds *Creds) (string, error) {
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return "", errors.New("not bearer challenge")
	}
	params := parseAuthHeaderParams(header[7:])
	realm := params["realm"]
	if realm == "" {
		return "", errors.New("missing realm")
	}
	u, err := url.Parse(realm)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	if sc := params["scope"]; sc != "" {
		q.Set("scope", sc)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if creds != nil && creds.Username != "" {
		req.SetBasicAuth(creds.Username, creds.Secret)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token status %d", resp.StatusCode)
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Token != "" {
		return payload.Token, nil
	}
	return payload.AccessToken, nil
}

func parseAuthHeaderParams(s string) map[string]string {
	out := make(map[string]string)
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return out
}

func classifyHTTPStatus(reg string, status int) *RegistryError {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &RegistryError{Kind: ErrAuthRequired, Message: fmt.Sprintf("仓库 %s 需要认证或无权限访问（HTTP %d）", reg, status)}
	case http.StatusNotFound:
		return &RegistryError{Kind: ErrNotFound, Message: fmt.Sprintf("仓库 %s 中未找到该镜像或标签（HTTP 404）", reg)}
	case http.StatusTooManyRequests:
		return &RegistryError{Kind: ErrRateLimited, Message: fmt.Sprintf("仓库 %s 请求触发限流（HTTP 429），请稍后重试或配置仓库账号", reg)}
	default:
		return &RegistryError{Kind: ErrRegistry, Message: fmt.Sprintf("仓库 %s 返回异常状态（HTTP %d）", reg, status)}
	}
}

func (c *Client) registryAPIBase(reg string) string {
	if c.BaseURLOverride != nil {
		if u, ok := c.BaseURLOverride[reg]; ok {
			return u
		}
	}
	if reg == "docker.io" || reg == "" {
		return "https://registry-1.docker.io"
	}
	return "https://" + reg
}

func (c *Client) hubAPIBase() string {
	if c.BaseURLOverride != nil {
		if u, ok := c.BaseURLOverride["hub.docker.com"]; ok {
			return u
		}
	}
	return "https://hub.docker.com"
}

func formatPlatform(osName, arch, variant string) string {
	p := strings.ToLower(osName) + "/" + strings.ToLower(arch)
	if variant != "" {
		p += "/" + strings.ToLower(variant)
	}
	return p
}

func (c *Client) getCachedToken(key string) string {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	t, ok := c.tokens[key]
	if !ok || time.Now().After(t.expiresAt) {
		return ""
	}
	return t.token
}

func (c *Client) setCachedToken(key, tok string) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.tokens[key] = cachedToken{token: tok, expiresAt: time.Now().Add(4 * time.Minute)}
}

func (c *Client) getCache(ctx context.Context, key string) ([]byte, bool) {
	if c.st == nil {
		return nil, false
	}
	var val []byte
	var exp int64
	err := c.st.DB.QueryRowContext(ctx, `SELECT v, expires_at FROM registry_cache WHERE k=?`, key).Scan(&val, &exp)
	if err != nil || time.Now().Unix() > exp {
		return nil, false
	}
	return val, true
}

func (c *Client) putCache(ctx context.Context, key string, val []byte, ttl time.Duration) {
	if c.st == nil {
		return
	}
	exp := time.Now().Add(ttl).Unix()
	_, _ = c.st.DB.ExecContext(ctx, `
		INSERT INTO registry_cache(k, v, expires_at) VALUES (?, ?, ?)
		ON CONFLICT(k) DO UPDATE SET v=excluded.v, expires_at=excluded.expires_at`,
		key, val, exp)
}

func (c *Client) getDigestTagCache(ctx context.Context, repo, digest string) []string {
	if c.st == nil || digest == "" {
		return nil
	}
	var raw string
	if err := c.st.DB.QueryRowContext(ctx, `SELECT tags_json FROM digest_tag_cache WHERE repo=? AND digest=?`, repo, digest).Scan(&raw); err != nil {
		return nil
	}
	var tags []string
	_ = json.Unmarshal([]byte(raw), &tags)
	return tags
}

func (c *Client) putDigestTagCache(ctx context.Context, repo, digest string, tags []string) {
	if c.st == nil || digest == "" || len(tags) == 0 {
		return
	}
	b, _ := json.Marshal(tags)
	_, _ = c.st.DB.ExecContext(ctx, `
		INSERT INTO digest_tag_cache(repo, digest, tags_json) VALUES (?, ?, ?)
		ON CONFLICT(repo, digest) DO UPDATE SET tags_json=excluded.tags_json`,
		repo, digest, string(b))
}

// ListCredentials 返回已配置的私有仓库凭证（不含密码明文）。
func (c *Client) ListCredentials(ctx context.Context) ([]Creds, error) {
	rows, err := c.st.DB.QueryContext(ctx, `SELECT registry, username FROM registry_credential ORDER BY registry ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Creds
	for rows.Next() {
		var cr Creds
		if err := rows.Scan(&cr.Registry, &cr.Username); err != nil {
			return nil, err
		}
		out = append(out, cr)
	}
	return out, rows.Err()
}

// SaveCredential 保存私有仓库账号密码。
func (c *Client) SaveCredential(ctx context.Context, in Creds) error {
	reg := strings.ToLower(strings.TrimSpace(in.Registry))
	if reg == "" || in.Username == "" || in.Secret == "" {
		return errors.New("请填写完整的仓库域名、用户名与密码/Token")
	}
	nonce, sealed, err := c.box.Seal([]byte(in.Secret))
	if err != nil {
		return err
	}
	secID := "regsec_" + reg
	if _, err := c.st.DB.ExecContext(ctx, `
		INSERT INTO secret(id, nonce, cipher) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET nonce=excluded.nonce, cipher=excluded.cipher`,
		secID, nonce, sealed); err != nil {
		return err
	}
	_, err = c.st.DB.ExecContext(ctx, `
		INSERT INTO registry_credential(registry, username, secret_ref) VALUES (?, ?, ?)
		ON CONFLICT(registry) DO UPDATE SET username=excluded.username, secret_ref=excluded.secret_ref`,
		reg, in.Username, secID)
	return err
}

// DeleteCredential 删除私有仓库凭证。
func (c *Client) DeleteCredential(ctx context.Context, reg string) error {
	reg = strings.ToLower(strings.TrimSpace(reg))
	_, _ = c.st.DB.ExecContext(ctx, `DELETE FROM secret WHERE id=?`, "regsec_"+reg)
	_, err := c.st.DB.ExecContext(ctx, `DELETE FROM registry_credential WHERE registry=?`, reg)
	return err
}

func (c *Client) getCredential(ctx context.Context, reg string) (*Creds, error) {
	if c.st == nil || c.box == nil {
		return nil, nil
	}
	var user, secRef string
	err := c.st.DB.QueryRowContext(ctx, `SELECT username, secret_ref FROM registry_credential WHERE registry=?`, strings.ToLower(reg)).Scan(&user, &secRef)
	if err != nil {
		return nil, err
	}
	var nonce, cipher []byte
	if err := c.st.DB.QueryRowContext(ctx, `SELECT nonce, cipher FROM secret WHERE id=?`, secRef).Scan(&nonce, &cipher); err != nil {
		return nil, err
	}
	plain, err := c.box.Open(nonce, cipher)
	if err != nil {
		return nil, err
	}
	return &Creds{Registry: reg, Username: user, Secret: string(plain)}, nil
}

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/version"
	"github.com/kukushouhou/DKStore/rules"
)

const (
	iconHitTTL       = 7 * 24 * time.Hour
	iconMissTTL      = 24 * time.Hour
	iconFetchTimeout = 4 * time.Second
	maxIconBytes     = 512 << 10 // 512 KiB
)

func (s *Server) handleGetIcon(w http.ResponseWriter, r *http.Request) {
	svcKey := strings.TrimSpace(r.URL.Query().Get("serviceKey"))
	if svcKey == "" || s.d.Store == nil || s.d.Reg == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	var imageRef, labelsJSON string
	err := s.d.Store.DB.QueryRowContext(r.Context(), `
		SELECT COALESCE(image_ref,''), COALESCE(labels_json,'')
		FROM service WHERE key=?`, svcKey).Scan(&imageRef, &labelsJSON)
	if err != nil || strings.TrimSpace(imageRef) == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	var labels map[string]string
	if labelsJSON != "" {
		_ = json.Unmarshal([]byte(labelsJSON), &labels)
	}

	customURL := pickCustomIconURL(labels)
	ref, refErr := version.ParseImageRef(imageRef)

	cacheKey := ""
	if customURL != "" {
		cacheKey = "icon:url:" + customURL
	} else if refErr == nil {
		cacheKey = "icon:" + ref.Registry + "/" + ref.Repository
	} else {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if cached, ok := s.d.Reg.GetCache(r.Context(), cacheKey); ok {
		if string(cached) == "none" {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if ct, imgBytes, ok := unpackCachedIcon(cached); ok {
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Cache-Control", "public, max-age=86400")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(imgBytes)
			return
		}
	}

	ct, imgBytes := s.resolveServiceIcon(r.Context(), ref, refErr == nil, customURL, labels)
	if len(imgBytes) == 0 {
		s.d.Reg.PutCache(r.Context(), cacheKey, []byte("none"), iconMissTTL)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	s.d.Reg.PutCache(r.Context(), cacheKey, packCachedIcon(ct, imgBytes), iconHitTTL)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(imgBytes)
}

func (s *Server) resolveServiceIcon(
	ctx context.Context,
	ref version.ParsedRef,
	hasRef bool,
	customURL string,
	labels map[string]string,
) (string, []byte) {
	cli := &http.Client{Timeout: iconFetchTimeout}

	// 1. 容器 / 镜像自定义图标 Label
	if customURL != "" {
		if ct, data := fetchImageURL(ctx, cli, customURL); len(data) > 0 {
			return ct, data
		}
	}

	if !hasRef {
		return "", nil
	}

	// 2. Docker Hub 官方 repos_logo 与命名空间头像
	if ref.Registry == "docker.io" {
		hubBase := strings.TrimRight(s.d.Reg.HubAPIBase(), "/")
		logoURL := hubBase + "/api/media/repos_logo/v1/" + url.PathEscape(ref.Repository)
		if ct, data := fetchImageURL(ctx, cli, logoURL); len(data) > 0 {
			return ct, data
		}
		ns, _, _ := strings.Cut(ref.Repository, "/")
		if ns != "" && ns != "library" {
			if gravURL := fetchHubNamespaceAvatarURL(ctx, cli, hubBase, ns); gravURL != "" {
				if ct, data := fetchImageURL(ctx, cli, gravURL); len(data) > 0 {
					return ct, data
				}
			}
		}
	}

	// 3. GitHub / GHCR / LSCR 组织或作者头像
	if owner := resolveGitHubOwner(ref, labels); owner != "" {
		ghBase := strings.TrimRight(s.d.Reg.GitHubBase(), "/")
		avatarURL := ghBase + "/" + url.PathEscape(owner) + ".png?size=128"
		if ct, data := fetchImageURL(ctx, cli, avatarURL); len(data) > 0 {
			return ct, data
		}
	}

	return "", nil
}

func pickCustomIconURL(labels map[string]string) string {
	for _, k := range []string{"net.unraid.docker.icon", "org.opencontainers.image.icon"} {
		u := strings.TrimSpace(labels[k])
		if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
			return u
		}
	}
	return ""
}

func resolveGitHubOwner(ref version.ParsedRef, labels map[string]string) string {
	switch strings.ToLower(ref.Registry) {
	case "ghcr.io":
		owner, _, _ := strings.Cut(ref.Repository, "/")
		return strings.TrimSpace(owner)
	case "lscr.io":
		if strings.HasPrefix(strings.ToLower(ref.Repository), "linuxserver/") {
			return "linuxserver"
		}
	}
	for _, rawSrc := range []string{
		labels["org.opencontainers.image.source"],
		labels["org.label-schema.vcs-url"],
		rules.Default().ResolveLinks(ref.Registry, ref.Repository, "").Repo,
	} {
		if owner := extractGitHubOwnerFromURL(rawSrc); owner != "" {
			return owner
		}
	}
	return ""
}

func extractGitHubOwnerFromURL(raw string) string {
	s := strings.TrimSpace(raw)
	idx := strings.Index(strings.ToLower(s), "github.com/")
	if idx < 0 {
		return ""
	}
	rest := s[idx+len("github.com/"):]
	owner, _, _ := strings.Cut(rest, "/")
	owner = strings.TrimSpace(strings.TrimSuffix(owner, ".git"))
	return owner
}

func fetchHubNamespaceAvatarURL(ctx context.Context, cli *http.Client, hubBase, ns string) string {
	reqCtx, cancel := context.WithTimeout(ctx, iconFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, hubBase+"/v2/users/"+url.PathEscape(ns)+"/", nil)
	if err != nil {
		return ""
	}
	resp, err := cli.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var payload struct {
		GravatarURL string `json:"gravatar_url"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&payload) != nil {
		return ""
	}
	u := strings.TrimSpace(payload.GravatarURL)
	if u == "" || (!strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://")) {
		return ""
	}
	if strings.Contains(strings.ToLower(u), "gravatar.com") && !strings.Contains(u, "d=") {
		if strings.Contains(u, "?") {
			u += "&d=404"
		} else {
			u += "?d=404"
		}
	}
	return u
}

func fetchImageURL(ctx context.Context, cli *http.Client, rawURL string) (string, []byte) {
	reqCtx, cancel := context.WithTimeout(ctx, iconFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil
	}
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	resp, err := cli.Do(req)
	if err != nil {
		return "", nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes))
	if err != nil || len(data) == 0 {
		return "", nil
	}
	ct := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if semi := strings.Index(ct, ";"); semi >= 0 {
		ct = strings.TrimSpace(ct[:semi])
	}
	if !strings.HasPrefix(strings.ToLower(ct), "image/") {
		sniffed := http.DetectContentType(data)
		if strings.HasPrefix(sniffed, "image/") {
			ct = sniffed
		} else if bytes.Contains(data[:min(len(data), 256)], []byte("<svg")) {
			ct = "image/svg+xml"
		} else {
			return "", nil
		}
	}
	return ct, data
}

func packCachedIcon(ct string, data []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString(ct)
	buf.WriteByte('\n')
	buf.Write(data)
	return buf.Bytes()
}

func unpackCachedIcon(raw []byte) (string, []byte, bool) {
	ct, body, ok := bytes.Cut(raw, []byte{'\n'})
	if !ok || len(ct) == 0 || len(body) == 0 {
		return "", nil, false
	}
	return string(ct), body, true
}

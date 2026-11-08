// Package selfupdate 提供 DKStore 自身版本检查与按发行包形态的安全自更新能力（立项文档 §15 / A16）。
package selfupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/version"
)

// Status 描述 DKStore 当前版本、最新可用版本与当前发行包形态的更新方式。
type Status struct {
	CurrentVersion string `json:"currentVersion"`
	Commit         string `json:"commit"`
	LatestVersion  string `json:"latestVersion"`
	HasUpdate      bool   `json:"hasUpdate"`
	InstallMode    string `json:"installMode"` // docker | openwrt | synology_spk | binary
	ReleaseURL     string `json:"releaseUrl"`
	PreserveNote   string `json:"preserveNote"`
	CommandHint    string `json:"commandHint"`
}

// Check 检查 DKStore 是否有新版本，并生成保留配置与历史数据的升级指引。
func Check(ctx context.Context, lang i18n.Lang) Status {
	cur := version.Version
	if cur == "" {
		cur = "v0.2.0"
	}
	mode := detectInstallMode()
	releaseURL := "https://github.com/kukushouhou/DKStore/releases"
	latest := cur
	hasUpdate := false

	hc := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/kukushouhou/DKStore/releases/latest", nil)
	if err == nil {
		req.Header.Set("Accept", "application/vnd.github+json")
		if resp, err := hc.Do(req); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var rel struct {
					TagName string `json:"tag_name"`
					HTMLURL string `json:"html_url"`
				}
				if json.NewDecoder(resp.Body).Decode(&rel) == nil && rel.TagName != "" {
					latest = rel.TagName
					if rel.HTMLURL != "" {
						releaseURL = rel.HTMLURL
					}
					pCur := version.ParseTag(cur)
					pLat := version.ParseTag(latest)
					if pCur.Scheme != version.SchemeUnknown && pLat.Scheme != version.SchemeUnknown &&
						version.Compare(pLat, pCur) > 0 {
						hasUpdate = true
					}
				}
			}
		}
	}

	note := "自更新过程中会自动保留数据目录（dkstore.db 与 secret.key），已连接的远程设备凭证、忽略规则与更新历史不会丢失。"
	if lang == i18n.EnUS {
		note = "Self-update preserves your data directory (dkstore.db & secret.key); connected host credentials, ignore rules, and update history remain intact."
	}

	var cmdHint string
	switch mode {
	case "docker":
		cmdHint = "docker compose pull dkstore && docker compose up -d dkstore"
	case "openwrt":
		cmdHint = "opkg update && opkg upgrade luci-app-dkstore  # or: apk upgrade luci-app-dkstore"
	case "synology_spk":
		if lang == i18n.EnUS {
			cmdHint = "Upgrade via Synology DSM Package Center -> Manual Install new .spk"
		} else {
			cmdHint = "在群晖 DSM「套件中心 → 手动安装」上传新版 .spk 完成覆盖升级（自动保留数据）"
		}
	default:
		cmdHint = "dkstore --version"
	}

	return Status{
		CurrentVersion: cur,
		Commit:         version.Commit,
		LatestVersion:  latest,
		HasUpdate:      hasUpdate,
		InstallMode:    mode,
		ReleaseURL:     releaseURL,
		PreserveNote:   note,
		CommandHint:    cmdHint,
	}
}

func detectInstallMode() string {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "docker"
	}
	if _, err := os.Stat("/var/packages/dkstore"); err == nil {
		return "synology_spk"
	}
	if _, err := os.Stat("/etc/openwrt_release"); err == nil {
		return "openwrt"
	}
	if strings.Contains(strings.ToLower(os.Getenv("CONTAINER")), "docker") {
		return "docker"
	}
	return "binary"
}

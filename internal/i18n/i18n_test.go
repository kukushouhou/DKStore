package i18n

import (
	"strings"
	"testing"
)

func TestNormalizeAndFormat(t *testing.T) {
	if Normalize("en-US,en;q=0.9") != EnUS {
		t.Fatal("expected EnUS")
	}
	if Normalize("zh-TW") != ZhCN {
		t.Fatal("expected ZhCN")
	}

	zhWarn := FormatReason(ZhCN, "database", "MySQL", "8.0.40", "8.0.41")
	enWarn := FormatReason(EnUS, "database", "MySQL", "8.0.40", "8.0.41")
	if !strings.Contains(zhWarn, "数据库") || !strings.Contains(enWarn, "database") {
		t.Fatalf("unexpected warnings: zh=%q en=%q", zhWarn, enWarn)
	}

	tZh, bZh := FormatNotification(ZhCN, []NotifyItem{
		{HostName: "Ubuntu", AppName: "nginx", Category: "normal", CurrentVersion: "1.27.1", TargetVersion: "1.27.2"},
		{HostName: "NAS", AppName: "mysql", Category: "caution", CurrentVersion: "8.0.40", TargetVersion: "8.0.41", Reason: "database", DatabaseName: "MySQL"},
	})
	if !strings.Contains(tZh, "2 个") || !strings.Contains(bZh, "需谨慎更新") {
		t.Fatalf("unexpected zh notification: %s / %s", tZh, bZh)
	}

	tEn, bEn := FormatNotification(EnUS, []NotifyItem{
		{HostName: "Ubuntu", AppName: "nginx", Category: "normal", CurrentVersion: "1.27.1", TargetVersion: "1.27.2"},
	})
	if !strings.Contains(tEn, "1 container update") || !strings.Contains(bEn, "Available Updates") {
		t.Fatalf("unexpected en notification: %s / %s", tEn, bEn)
	}
}

func TestFormatUpdateError_HumanizedPlainLanguage(t *testing.T) {
	cases := []struct {
		code   string
		detail string
		wantZh string
		wantEn string
	}{
		{"pull_failed", "EOF", "网络连接中途断开，已下载的部分会保留", "network connection dropped"},
		{"pull_failed", "homeassistant/home-assistant:latest — EOF", "网络连接中途断开，已下载的部分会保留", "network connection dropped"},
		{"pull_failed", "unexpected EOF", "网络连接中途断开，已下载的部分会保留", "network connection dropped"},
		{"pull_failed", "HTTP 500: {\"message\":\"Get \\\"https://registry-1.docker.io/v2/\\\": EOF\"}", "无法访问容器仓库", "Cannot access container registry"},
		{"pull_failed", "Get \"https://registry-1.docker.io/v2/\": net/http: TLS handshake timeout", "访问容器仓库超时", "Container registry timed out"},
		{"pull_failed", "toomanyrequests: You have reached your pull rate limit", "临时限流", "rate-limited"},
		{"pull_failed", "unauthorized: authentication required", "需要登录或当前账号无访问权限", "authentication required"},
		{"pull_failed", "manifest unknown", "找不到该版本镜像", "version not found"},
		{"pull_failed", "write /var/lib/docker: no space left on device", "设备存储空间不足", "Insufficient disk space"},
		{"recreate_failed", "Bind for 0.0.0.0:8123 failed: port is already allocated", "所需端口已被其他程序占用", "port is already in use"},
	}
	for _, c := range cases {
		zh := FormatUpdateError(ZhCN, c.code, c.detail)
		en := FormatUpdateError(EnUS, c.code, c.detail)
		if !strings.Contains(zh, c.wantZh) || strings.Contains(zh, "EOF") {
			t.Fatalf("FormatUpdateError(ZhCN, %q, %q) = %q, want substring %q without raw EOF", c.code, c.detail, zh, c.wantZh)
		}
		if !strings.Contains(en, c.wantEn) {
			t.Fatalf("FormatUpdateError(EnUS, %q, %q) = %q, want substring %q", c.code, c.detail, en, c.wantEn)
		}
	}

	// 容器或镜像名包含 eof 子串（如 geofence）不应误判为网络断流 EOF
	nonEOF := FormatUpdateError(ZhCN, "pull_failed", "custom error on geofence image")
	if strings.Contains(nonEOF, "网络连接中途断开") {
		t.Fatalf("unexpected false-positive EOF match on geofence: %q", nonEOF)
	}

	// 镜像仓库检测网络报错同样不再暴露裸 EOF
	regMsg := EngineMessage(ZhCN, MsgRegistryNetwork, "Docker Hub", `Get "https://registry-1.docker.io/v2/": EOF`)
	if !strings.Contains(regMsg, "网络连接中途断开") || strings.Contains(regMsg, "EOF") {
		t.Fatalf("expected humanized registry network error, got %q", regMsg)
	}
}

func TestBilingualAndLegacyStrip(t *testing.T) {
	msg := Bilingual("群晖项目重建失败：invalid mount path", "Synology Project rebuild failed: invalid mount path") +
		" | " + Bilingual("已写回原 YAML", "original YAML restored")
	zh := FormatUpdateError(ZhCN, "recreate_failed", msg)
	en := FormatUpdateError(EnUS, "recreate_failed", msg)
	if strings.Contains(zh, "Synology Project") || strings.Contains(zh, "original YAML") || !strings.Contains(zh, "群晖项目重建失败") {
		t.Fatalf("unexpected ZhCN output: %q", zh)
	}
	if strings.Contains(en, "群晖") || strings.Contains(en, "已写回") || !strings.Contains(en, "Synology Project rebuild failed") {
		t.Fatalf("unexpected EnUS output: %q", en)
	}

	legacy := "群晖 Project 重建失败: Error response from daemon: invalid mount path: '--net=host' / Synology Project rebuild failed: Error response from daemon: invalid mount path: '--net=host' | 已写回原 YAML / original YAML restored"
	lzh := FormatUpdateError(ZhCN, "recreate_failed", legacy)
	lenOut := FormatUpdateError(EnUS, "recreate_failed", legacy)
	if strings.Contains(lzh, "Synology Project") || strings.Contains(lzh, "original YAML") {
		t.Fatalf("expected legacy English half stripped for ZhCN, got %q", lzh)
	}
	if strings.Contains(lenOut, "群晖") || strings.Contains(lenOut, "已写回") {
		t.Fatalf("expected legacy Chinese half stripped for EnUS, got %q", lenOut)
	}
}

func TestFormatStructuredUpdateError(t *testing.T) {
	// 1) 已配置镜像源时：统一报「无法访问容器仓库」，简明提示可能是已配置镜像源异常（不列具体 URL），并提供 mirror 快捷动作
	stWithMirror := FormatStructuredUpdateError(
		ZhCN,
		"pull_failed",
		`HTTP 500: {"message":"Get \"https://registry-1.docker.io/v2/\": EOF"}`,
		"软路由模拟器",
		0,
		[]string{"https://docker.mirrors.ustc.edu.cn", "https://docker.1ms.run"},
	)
	if stWithMirror.Title != "无法访问容器仓库" || stWithMirror.Action != "mirror" || stWithMirror.Detail != "请检查设备网络连接，也可能是已配置的镜像源异常" {
		t.Fatalf("unexpected structured error with mirrors: %+v", stWithMirror)
	}

	// 2) 未配置任何镜像源时：正常报「无法访问容器仓库」，不提镜像源且不返回 mirror 动作
	stNoMirror := FormatStructuredUpdateError(
		ZhCN,
		"pull_failed",
		`HTTP 500: {"message":"Get \"https://registry-1.docker.io/v2/\": EOF"}`,
		"软路由模拟器",
		0,
		nil,
	)
	if stNoMirror.Title != "无法访问容器仓库" || stNoMirror.Action != "" || stNoMirror.Detail != "请检查设备网络连接，或稍后重试" {
		t.Fatalf("unexpected structured error without mirrors: %+v", stNoMirror)
	}

	drop := FormatStructuredUpdateError(ZhCN, "pull_failed", "unexpected EOF", "软路由模拟器", 4096, nil)
	if drop.Title != "镜像下载中断" || drop.Action != "" {
		t.Fatalf("unexpected structured drop error: %+v", drop)
	}

	auth := FormatStructuredUpdateError(ZhCN, "pull_failed", "unauthorized: authentication required", "NAS", 0, nil)
	if auth.Title != "仓库需要登录凭据" || auth.Action != "cred" {
		t.Fatalf("unexpected structured auth error: %+v", auth)
	}
}

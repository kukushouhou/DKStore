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

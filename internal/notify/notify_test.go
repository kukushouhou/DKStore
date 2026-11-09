package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/store"
)

func TestInQuietHours(t *testing.T) {
	at := func(h, m int) time.Time {
		return time.Date(2026, 11, 9, h, m, 0, 0, time.Local)
	}

	// Cross-midnight window: 23:00 -> 07:00
	if !inQuietHours(at(23, 30), "23:00", "07:00") {
		t.Fatal("expected 23:30 to be inside 23:00-07:00")
	}
	if !inQuietHours(at(2, 15), "23:00", "07:00") {
		t.Fatal("expected 02:15 to be inside 23:00-07:00")
	}
	if inQuietHours(at(7, 0), "23:00", "07:00") {
		t.Fatal("expected 07:00 to be outside 23:00-07:00")
	}
	if inQuietHours(at(12, 0), "23:00", "07:00") {
		t.Fatal("expected 12:00 to be outside 23:00-07:00")
	}

	// Same-day window: 09:00 -> 18:00
	if !inQuietHours(at(10, 0), "09:00", "18:00") {
		t.Fatal("expected 10:00 to be inside 09:00-18:00")
	}
	if inQuietHours(at(20, 0), "09:00", "18:00") {
		t.Fatal("expected 20:00 to be outside 09:00-18:00")
	}
}

func TestSendTest_LocalizedValidationErrors(t *testing.T) {
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := New(st)

	errZh := svc.SendTest(context.Background(), &Config{Channel: "webhook", WebhookURL: ""}, i18n.ZhCN)
	if errZh == nil || !strings.Contains(errZh.Error(), "Webhook") {
		t.Fatalf("expected localized zh error, got %v", errZh)
	}
	errEn := svc.SendTest(context.Background(), &Config{Channel: "telegram"}, i18n.EnUS)
	if errEn == nil || !strings.Contains(errEn.Error(), "Telegram Bot Token") {
		t.Fatalf("expected localized en error, got %v", errEn)
	}
}

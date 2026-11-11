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

func TestProgressStylePersistence_DefaultAndPreservation(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := New(st)

	// Default is "layered"
	if got := svc.LoadConfig(ctx).ProgressStyle; got != "layered" {
		t.Fatalf("expected default progressStyle=layered, got %q", got)
	}

	// Save "unified" and verify setting table key "progress_style"
	if err := svc.SaveConfig(ctx, Config{Locale: "en-US", ProgressStyle: "unified", CheckIntervalHours: 12}); err != nil {
		t.Fatal(err)
	}
	var dbVal string
	if err := st.DB.QueryRowContext(ctx, `SELECT v FROM setting WHERE k='progress_style'`).Scan(&dbVal); err != nil || dbVal != "unified" {
		t.Fatalf("expected setting.progress_style=unified, got %q (err=%v)", dbVal, err)
	}

	// Saving full config without progressStyle preserves "unified"
	if err := svc.SaveConfig(ctx, Config{Locale: "zh-CN", CheckIntervalHours: 6}); err != nil {
		t.Fatal(err)
	}
	loaded := svc.LoadConfig(ctx)
	if loaded.ProgressStyle != "unified" || loaded.Locale != "zh-CN" {
		t.Fatalf("expected progressStyle=unified preserved, got %+v", loaded)
	}
}

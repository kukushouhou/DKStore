// Package notify 实现多渠道更新提醒推送（Webhook / Bark / 企业微信 / Telegram / ntfy），支持中英双语（实施文档 §8.2 / §10.4）。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/store"
)

// Config 是通知推送与全局偏好的设置结构（保存在 setting 表）。
type Config struct {
	Locale              string `json:"locale"`              // "zh-CN" | "en-US"
	CheckIntervalHours  int    `json:"checkIntervalHours"`  // 0=关闭, 1/3/6/12/24，默认 6
	Channel             string `json:"channel"`             // "" | "webhook" | "bark" | "wecom" | "telegram" | "ntfy"
	WebhookURL          string `json:"webhookUrl"`          // Webhook / Bark / 企业微信 / ntfy 地址
	TelegramBotToken    string `json:"telegramBotToken"`    // Telegram Bot Token
	TelegramChatID      string `json:"telegramChatId"`      // Telegram Chat ID
	QuietHoursStart     string `json:"quietHoursStart"`     // 如 "23:00"，可选
	QuietHoursEnd       string `json:"quietHoursEnd"`       // 如 "08:00"，可选
}

// Service 管理设置读写与通知发送。
type Service struct {
	st         *store.Store
	httpClient *http.Client
}

// New 创建通知与设置服务。
func New(st *store.Store) *Service {
	return &Service{
		st:         st,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

const settingKey = "app_config"

// LoadConfig 读取当前设置（未设置时返回默认值）。
func (s *Service) LoadConfig(ctx context.Context) Config {
	cfg := Config{
		Locale:             string(i18n.ZhCN),
		CheckIntervalHours: 6,
	}
	var raw string
	if err := s.st.DB.QueryRowContext(ctx, `SELECT v FROM setting WHERE k=?`, settingKey).Scan(&raw); err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &cfg)
	}
	cfg.Locale = string(i18n.Normalize(cfg.Locale))
	if cfg.CheckIntervalHours < 0 {
		cfg.CheckIntervalHours = 6
	}
	return cfg
}

// SaveConfig 保存设置。
func (s *Service) SaveConfig(ctx context.Context, cfg Config) error {
	cfg.Locale = string(i18n.Normalize(cfg.Locale))
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = s.st.DB.ExecContext(ctx, `
		INSERT INTO setting(k, v) VALUES (?, ?)
		ON CONFLICT(k) DO UPDATE SET v=excluded.v`,
		settingKey, string(b))
	return err
}

// SendUpdates 按系统语言发送新发现的更新汇总。
func (s *Service) SendUpdates(ctx context.Context, items []i18n.NotifyItem) error {
	if len(items) == 0 {
		return nil
	}
	cfg := s.LoadConfig(ctx)
	if cfg.Channel == "" {
		return nil
	}
	title, body := i18n.FormatNotification(i18n.Normalize(cfg.Locale), items)
	return s.dispatch(ctx, cfg, title, body)
}

// SendTest 发送一条测试通知（优先使用传入的 cfg，若 Channel 为空则读取已保存的设置）。
func (s *Service) SendTest(ctx context.Context, override *Config, lang i18n.Lang) error {
	cfg := s.LoadConfig(ctx)
	if override != nil && override.Channel != "" {
		cfg = *override
	}
	if lang == "" {
		lang = i18n.Normalize(cfg.Locale)
	}
	title, body := i18n.FormatTestNotification(lang)
	return s.dispatch(ctx, cfg, title, body)
}

func (s *Service) dispatch(ctx context.Context, cfg Config, title, body string) error {
	switch cfg.Channel {
	case "webhook":
		return s.postJSON(ctx, cfg.WebhookURL, map[string]any{
			"title":   title,
			"message": body,
			"source":  "dkstore",
			"time":    time.Now().Unix(),
		})
	case "wecom":
		return s.postJSON(ctx, cfg.WebhookURL, map[string]any{
			"msgtype": "text",
			"text": map[string]string{
				"content": title + "\n\n" + body,
			},
		})
	case "bark":
		u := strings.TrimRight(cfg.WebhookURL, "/") + "/" + url.PathEscape(title) + "/" + url.PathEscape(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		resp, err := s.httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("Bark HTTP %d", resp.StatusCode)
		}
		return nil
	case "telegram":
		if cfg.TelegramBotToken == "" || cfg.TelegramChatID == "" {
			return errors.New("Telegram Bot Token / Chat ID cannot be empty")
		}
		u := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", cfg.TelegramBotToken)
		return s.postJSON(ctx, u, map[string]any{
			"chat_id": cfg.TelegramChatID,
			"text":    title + "\n\n" + body,
		})
	case "ntfy":
		if cfg.WebhookURL == "" {
			return errors.New("ntfy URL cannot be empty")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.WebhookURL, strings.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Title", title)
		resp, err := s.httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("ntfy HTTP %d", resp.StatusCode)
		}
		return nil
	default:
		return errors.New("notification channel not configured")
	}
}

func (s *Service) postJSON(ctx context.Context, targetURL string, payload any) error {
	if strings.TrimSpace(targetURL) == "" {
		return errors.New("webhook URL is empty")
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

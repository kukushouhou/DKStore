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
	"strconv"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/store"
)

// ErrQuietHours 表示当前处于免打扰时段，暂缓发送推送（保留待通知状态，免打扰结束后再补推）。
var ErrQuietHours = errors.New("notification skipped during quiet hours")

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

// SendUpdates 按系统语言发送新发现的更新汇总；若处于免打扰时段则返回 ErrQuietHours。
func (s *Service) SendUpdates(ctx context.Context, items []i18n.NotifyItem) error {
	if len(items) == 0 {
		return nil
	}
	cfg := s.LoadConfig(ctx)
	if cfg.Channel == "" {
		return nil
	}
	if inQuietHours(time.Now(), cfg.QuietHoursStart, cfg.QuietHoursEnd) {
		return ErrQuietHours
	}
	lang := i18n.Normalize(cfg.Locale)
	title, body := i18n.FormatNotification(lang, items)
	return s.dispatch(ctx, cfg, lang, title, body)
}

// inQuietHours 判断当前本地时间是否处于 [start, end) 免打扰区间（支持跨零点，如 "23:00" 至 "08:00"）。
func inQuietHours(now time.Time, startStr, endStr string) bool {
	startMin, ok1 := parseHHMM(startStr)
	endMin, ok2 := parseHHMM(endStr)
	if !ok1 || !ok2 || startMin == endMin {
		return false
	}
	curMin := now.Hour()*60 + now.Minute()
	if startMin < endMin {
		return curMin >= startMin && curMin < endMin
	}
	return curMin >= startMin || curMin < endMin
}

func parseHHMM(s string) (int, bool) {
	hStr, mStr, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, false
	}
	h, err1 := strconv.Atoi(hStr)
	m, err2 := strconv.Atoi(mStr)
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
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
	return s.dispatch(ctx, cfg, lang, title, body)
}

func (s *Service) dispatch(ctx context.Context, cfg Config, lang i18n.Lang, title, body string) error {
	switch cfg.Channel {
	case "webhook":
		return s.postJSON(ctx, cfg.WebhookURL, lang, map[string]any{
			"title":   title,
			"message": body,
			"source":  "dkstore",
			"time":    time.Now().Unix(),
		})
	case "wecom":
		return s.postJSON(ctx, cfg.WebhookURL, lang, map[string]any{
			"msgtype": "text",
			"text": map[string]string{
				"content": title + "\n\n" + body,
			},
		})
	case "bark":
		if strings.TrimSpace(cfg.WebhookURL) == "" {
			return notifyValidationErr(lang, "请填写 Bark 推送地址", "Bark URL cannot be empty")
		}
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
			return notifyValidationErr(lang, "请填写 Telegram Bot Token 与 Chat ID", "Telegram Bot Token / Chat ID cannot be empty")
		}
		u := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", cfg.TelegramBotToken)
		return s.postJSON(ctx, u, lang, map[string]any{
			"chat_id": cfg.TelegramChatID,
			"text":    title + "\n\n" + body,
		})
	case "ntfy":
		if strings.TrimSpace(cfg.WebhookURL) == "" {
			return notifyValidationErr(lang, "请填写 ntfy 推送地址", "ntfy URL cannot be empty")
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
		return notifyValidationErr(lang, "尚未选择通知推送渠道", "Notification channel is not configured")
	}
}

func (s *Service) postJSON(ctx context.Context, targetURL string, lang i18n.Lang, payload any) error {
	if strings.TrimSpace(targetURL) == "" {
		return notifyValidationErr(lang, "请填写 Webhook 推送地址", "Webhook URL cannot be empty")
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

func notifyValidationErr(lang i18n.Lang, zh, en string) error {
	if lang == i18n.EnUS {
		return errors.New(en)
	}
	return errors.New(zh)
}

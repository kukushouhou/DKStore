package updater

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
)

// AppLifecycleSubmitRequest 是 POST /api/v1/apps/lifecycle 的请求结构。
type AppLifecycleSubmitRequest struct {
	AppKey        string `json:"appKey"`
	ContainerID   string `json:"containerId,omitempty"`
	ContainerName string `json:"containerName,omitempty"`
	ServiceName   string `json:"serviceName,omitempty"`
	Action        string `json:"action"` // start | stop | restart
}

// ControlLifecycle 执行单个容器或整个项目的启动、停止或重启，并同步刷新库存状态与运行态快照。
func (s *Service) ControlLifecycle(ctx context.Context, req AppLifecycleSubmitRequest, lang i18n.Lang) error {
	appKey := strings.TrimSpace(req.AppKey)
	action := strings.ToLower(strings.TrimSpace(req.Action))
	switch action {
	case string(platform.ActionStart), string(platform.ActionStop), string(platform.ActionRestart):
	default:
		return errors.New("invalid lifecycle action")
	}

	hostID, appName, source, proj, err := s.resolveAppTarget(ctx, appKey, lang)
	if err != nil {
		return err
	}

	s.mu.Lock()
	for _, j := range s.activeJobs {
		if j.AppKey == appKey && (j.Status == "running" || j.Status == "queued") {
			s.mu.Unlock()
			if lang == i18n.EnUS {
				return errors.New("An update is currently running for this app; please wait until it finishes")
			}
			return errors.New("当前应用正在执行更新任务，请等待更新完成后再操作")
		}
	}
	s.mu.Unlock()

	unlock := s.lockApp(appKey)
	defer unlock()

	ad, _, err := s.hosts.OpenAdapter(ctx, hostID)
	if err != nil {
		return err
	}
	defer ad.Close()

	lcAd, ok := ad.(platform.LifecycleAdapter)
	if !ok {
		return i18n.NewEngineError(i18n.MsgUpdateAdapterNoUpdate)
	}
	proj = enrichDSMProjectIfNeeded(ctx, ad, appKey, source, proj)

	if err := lcAd.ControlLifecycle(ctx, platform.LifecycleRequest{
		AppKey:        appKey,
		AppName:       appName,
		Source:        source,
		Project:       proj,
		ContainerID:   strings.TrimSpace(req.ContainerID),
		ContainerName: strings.TrimSpace(req.ContainerName),
		ServiceName:   strings.TrimSpace(req.ServiceName),
		Action:        platform.LifecycleAction(action),
	}); err != nil {
		var ue *platform.UpdateError
		if errors.As(err, &ue) {
			return errors.New(i18n.FormatUpdateError(lang, ue.Code, i18n.LocalizeDetail(lang, ue.Message)))
		}
		return err
	}

	sCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if s.inv != nil {
		_, _, _ = s.inv.SyncHost(sCtx, hostID)
	}
	if s.hosts != nil {
		_, _ = s.hosts.ListRuntimeContainers(sCtx, hostID, false)
		s.hosts.RefreshRuntimeAsync(hostID)
	}
	return nil
}

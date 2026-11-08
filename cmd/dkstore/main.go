// DKStore：家庭 Docker 应用更新中心。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kukushouhou/DKStore/internal/api"
	"github.com/kukushouhou/DKStore/internal/auth"
	"github.com/kukushouhou/DKStore/internal/checker"
	"github.com/kukushouhou/DKStore/internal/config"
	"github.com/kukushouhou/DKStore/internal/host"
	"github.com/kukushouhou/DKStore/internal/ignore"
	"github.com/kukushouhou/DKStore/internal/inventory"
	"github.com/kukushouhou/DKStore/internal/notify"
	"github.com/kukushouhou/DKStore/internal/registry"
	"github.com/kukushouhou/DKStore/internal/secret"
	"github.com/kukushouhou/DKStore/internal/store"
	"github.com/kukushouhou/DKStore/internal/version"
	"github.com/kukushouhou/DKStore/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dkstore:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	log.Info("DKStore 启动", "version", version.Version, "commit", version.Commit, "data", cfg.DataDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()

	box, err := secret.LoadOrCreate(cfg.DataDir)
	if err != nil {
		return err
	}

	authSvc := auth.New(st)
	hostReg := host.NewRegistry(st, box)
	_ = hostReg.EnsureLocalHost(ctx)

	invSvc := inventory.New(st, hostReg)
	regCli := registry.New(st, box)
	ignSvc := ignore.New(st)
	ntfSvc := notify.New(st)
	chkSvc := checker.New(log, st, hostReg, invSvc, regCli, ignSvc, ntfSvc)

	chkSvc.StartScheduler(ctx)
	// 启动时若从未检查过且存在主机，自动后台触发一次初始检查
	go func() {
		time.Sleep(1 * time.Second)
		hosts, _ := hostReg.List(ctx)
		if len(hosts) > 0 {
			var count int
			_ = st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM check_run`).Scan(&count)
			if count == 0 {
				chkSvc.Trigger("startup", "", false)
			}
		}
	}()

	assets, ok := web.Assets()
	if cfg.DevMode || !ok {
		assets = nil
		log.Warn("未使用内嵌前端（开发模式或前端未构建）")
	}

	srv := &http.Server{
		Addr: cfg.Listen,
		Handler: api.New(api.Deps{
			Log:     log,
			Store:   st,
			Auth:    authSvc,
			Hosts:   hostReg,
			Reg:     regCli,
			Ignore:  ignSvc,
			Notify:  ntfSvc,
			Checker: chkSvc,
			Assets:  assets,
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("HTTP 服务监听", "addr", cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("正在停止…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}

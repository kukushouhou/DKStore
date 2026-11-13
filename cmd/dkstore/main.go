// DKStore：家庭 Docker 应用更新中心。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kukushouhou/DKStore/internal/api"
	"github.com/kukushouhou/DKStore/internal/auth"
	"github.com/kukushouhou/DKStore/internal/checker"
	"github.com/kukushouhou/DKStore/internal/config"
	"github.com/kukushouhou/DKStore/internal/host"
	"github.com/kukushouhou/DKStore/internal/hostauth"
	"github.com/kukushouhou/DKStore/internal/ignore"
	"github.com/kukushouhou/DKStore/internal/inventory"
	"github.com/kukushouhou/DKStore/internal/notify"
	"github.com/kukushouhou/DKStore/internal/registry"
	"github.com/kukushouhou/DKStore/internal/secret"
	"github.com/kukushouhou/DKStore/internal/store"
	"github.com/kukushouhou/DKStore/internal/updater"
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
	if len(os.Args) >= 2 && (os.Args[1] == "--rpcd" || os.Args[1] == "-rpcd") {
		return runRPCD(os.Args[2:])
	}
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

	authSvc := auth.New(st, nil) // nil：按运行环境自动判定安装方式并构建宿主登录态核验器
	hostReg := host.NewRegistry(st, box)
	defer hostReg.Close()
	_ = hostReg.EnsureLocalHost(ctx)
	hostReg.EnableBackgroundCollector(ctx)

	invSvc := inventory.New(st, hostReg)
	regCli := registry.New(st, box)
	ignSvc := ignore.New(st)
	ntfSvc := notify.New(st)
	chkSvc := checker.New(log, st, hostReg, invSvc, regCli, ignSvc, ntfSvc)
	upSvc := updater.New(log, st, hostReg, invSvc, regCli)

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

	handler := api.New(api.Deps{
		Log:     log,
		Store:   st,
		Auth:    authSvc,
		Hosts:   hostReg,
		Reg:     regCli,
		Ignore:  ignSvc,
		Notify:  ntfSvc,
		Checker: chkSvc,
		Updater: upSvc,
		Assets:  assets,
	}).Handler()

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	var unixSrv *http.Server
	if cfg.UnixSocket != "" {
		_ = os.Remove(cfg.UnixSocket)
		if err := os.MkdirAll(filepath.Dir(cfg.UnixSocket), 0o755); err != nil {
			return fmt.Errorf("创建 Unix Socket 目录失败: %w", err)
		}
		unixLn, err := net.Listen("unix", cfg.UnixSocket)
		if err != nil {
			return fmt.Errorf("监听 Unix Socket (%s) 失败: %w", cfg.UnixSocket, err)
		}
		sockPerm := os.FileMode(0o666)
		if hostauth.DetectInstallMode(hostauth.OSFS()) == hostauth.ModeOpenWrt {
			sockPerm = 0o600
		}
		_ = os.Chmod(cfg.UnixSocket, sockPerm)
		unixSrv = &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			ConnContext: func(c context.Context, _ net.Conn) context.Context {
				return hostauth.WithTrustedGateway(c)
			},
		}
		go func() {
			log.Info("Unix Socket 网关监听", "socket", cfg.UnixSocket)
			if err := unixSrv.Serve(unixLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("Unix Socket 服务异常退出", "err", err)
			}
		}()
	}

	errCh := make(chan error, 1)
	if cfg.Listen != "" {
		go func() {
			log.Info("HTTP 服务监听", "addr", cfg.Listen)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
			close(errCh)
		}()
	}

	select {
	case err, ok := <-errCh:
		if ok && err != nil {
			return err
		}
	case <-ctx.Done():
	}

	log.Info("正在停止…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if unixSrv != nil {
		_ = unixSrv.Shutdown(shutdownCtx)
		_ = os.Remove(cfg.UnixSocket)
	}
	if cfg.Listen != "" {
		return srv.Shutdown(shutdownCtx)
	}
	return nil
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

// 认字练习 —— 给小孩认汉字用的网页程序。
//
// 出一个字，孩子点「认识」或「不认识」，程序按艾宾浩斯曲线安排复习。
// 数据存在本地 SQLite 文件里，孩子用平板通过局域网浏览器访问。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"chinese-learn/internal/schedule"
	"chinese-learn/internal/store"
	"chinese-learn/internal/web"
)

func main() {
	var (
		addr     = flag.String("addr", "0.0.0.0:8080", "监听地址")
		dbPath   = flag.String("db", "data/chinese.db", "SQLite 数据库文件")
		dev      = flag.Bool("dev", false, "开发模式：模板与静态资源从磁盘读取，改完刷新即见")
		logLevel = flag.String("log", "info", "日志级别：debug / info / warn / error")
	)
	flag.Parse()

	log := newLogger(*logLevel)

	if err := run(*addr, *dbPath, *dev, log); err != nil {
		log.Error("启动失败", "err", err)
		os.Exit(1)
	}
}

func run(addr, dbPath string, dev bool, log *slog.Logger) error {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("创建数据目录: %w", err)
	}

	st, err := store.Open(store.Options{Path: dbPath})
	if err != nil {
		return err
	}
	defer st.Close()

	// 首次启动自动建好默认孩子和学期，用户打开页面就能直接用。
	if err := st.EnsureSeed(); err != nil {
		return fmt.Errorf("初始化数据: %w", err)
	}

	// 日切点从设置里读，让「几点算新的一天」可调。
	if children, err := st.ListChildren(); err == nil && len(children) > 0 {
		if cfg, err := st.GetSettings(children[0].ID); err == nil {
			st.SetCutoff(cfg.Cutoff())
		}
	}

	srv, err := web.New(web.Options{
		Store:     st,
		Params:    schedule.DefaultParams(),
		Dev:       dev,
		Templates: "internal/web/templates",
		StaticDir: "internal/web/static",
		Logger:    log,
	})
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// 优雅关闭：收到信号后给在途请求 10 秒完成。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("服务已启动", "addr", addr, "db", dbPath, "dev", dev)
		logLanHint(log, addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("正在关闭…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}

// logLanHint 打印局域网访问地址，方便家长在平板上打开。
func logLanHint(log *slog.Logger, addr string) {
	_, port, err := splitHostPort(addr)
	if err != nil {
		return
	}
	for _, ip := range lanIPs() {
		log.Info("平板/手机可以访问", "url", "http://"+ip+":"+port)
	}
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
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv})
	return slog.New(h)
}

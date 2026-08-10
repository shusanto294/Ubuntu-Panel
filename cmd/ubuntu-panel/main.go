// Command ubuntu-panel is the panel's HTTP server.
//
// It is one static binary with the frontend embedded in it. Nothing is
// compiled on the machine it runs on, and there is no runtime to install
// beside it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/assets"
	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/session"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/web"
)

func main() {
	configPath := flag.String("config", "", "path to panel.conf (default /etc/ubuntu-panel/panel.conf)")
	showVersion := flag.Bool("version", false, "print the version and exit")

	flag.Parse()

	if *showVersion {
		fmt.Println(config.Name, config.Version)
		return
	}

	if err := run(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, "ubuntu-panel:", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("reading the configuration: %w", err)
	}

	log, closeLog := logger(cfg)
	defer closeLog()

	log.Info("starting", "panel", config.Name, "version", config.Version)

	db, err := store.Open(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()

	log.Info("database ready", "path", cfg.DatabasePath)

	app := web.New(cfg, db, assets.FS(cfg.PublicPath, cfg.Debug), log)

	if cfg.Debug {
		log.Warn("debug mode: the frontend is served from disk", "path", assets.DiskPath(cfg.PublicPath))
	}

	count, err := db.UserCount(context.Background())
	if err != nil {
		return fmt.Errorf("counting accounts: %w", err)
	}
	if count == 0 {
		log.Info("no account exists yet — open the panel and it will ask you to create one")
	}

	server := &http.Server{
		Addr:    cfg.Address(),
		Handler: app.Handler(),

		// A control panel shells out to installers that take minutes, so the
		// write timeout is generous. The read timeouts are not: a client that
		// opens a connection and sends nothing is either broken or hostile.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	// Housekeeping that has to happen but not on a request's critical path.
	background, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()

	go prune(background, app.Sessions, log)

	errs := make(chan error, 1)

	go func() {
		log.Info("listening", "address", cfg.Address())

		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errs:
		return fmt.Errorf("serving: %w", err)
	case <-stop:
	}

	log.Info("shutting down")

	// Let a request that is halfway through installing something finish rather
	// than leaving a half-configured service behind.
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return server.Shutdown(shutdown)
}

// prune clears out sessions nobody has used, hourly.
func prune(ctx context.Context, sessions *session.Manager, log *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	sessions.Prune(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sessions.Prune(ctx)
		}
	}
}

// logger writes to stderr, which systemd captures, and to the log file when
// one can be opened. A log file that cannot be written is not a reason to
// refuse to start: the panel is often what you would use to fix that.
func logger(cfg *config.Config) (*slog.Logger, func()) {
	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}

	options := &slog.HandlerOptions{Level: level}

	if directory := filepath.Dir(cfg.LogPath); directory != "." {
		if err := os.MkdirAll(directory, 0o750); err == nil {
			file, err := os.OpenFile(cfg.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
			if err == nil {
				log := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, file), options))

				return log, func() { file.Close() }
			}
		}
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, options))

	log.Warn("no log file; logging to stderr only", "path", cfg.LogPath)

	return log, func() {}
}

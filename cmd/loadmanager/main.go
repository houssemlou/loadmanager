// Command loadmanager runs the station load-management HTTP service.
//
// Environment:
//
//	CONFIG_PATH  station config JSON (default configs/example-station.json)
//	LISTEN_ADDR  listen address      (default :8080)
//	GIN_MODE     gin mode            (default release)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/houssemlou/loadmanager/internal/api"
	"github.com/houssemlou/loadmanager/internal/config"
	"github.com/houssemlou/loadmanager/internal/station"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfgPath := envOr("CONFIG_PATH", "configs/example-station.json")
	addr := envOr("LISTEN_ADDR", ":8080")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err // fail fast: never run with a config we don't understand
	}
	log.Info("config loaded", "path", cfgPath, "station_id", cfg.StationID,
		"grid_capacity_kw", cfg.GridCapacityKW, "chargers", len(cfg.Chargers))

	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	// Cancelled on SIGINT/SIGTERM; also the base context of every request,
	// so long-lived SSE streams end and Shutdown doesn't wait for them.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              addr,
		Handler:           api.NewRouter(station.New(cfg, log), log),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       60 * time.Second,
		// No WriteTimeout: it would cut the SSE setpoint stream.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

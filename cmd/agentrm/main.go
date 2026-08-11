package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/api"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/backend"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/controller"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/queue"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/scheduler"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/store"
)

func main() {
	listenAddress := flag.String("listen", ":8080", "HTTP listen address")
	capacityCPU := flag.Int64("capacity-cpu-milli", 16000, "cluster CPU capacity in millicores")
	capacityMemoryMiB := flag.Int64("capacity-memory-mib", 32768, "cluster memory capacity in MiB")
	reconcileInterval := flag.Duration("reconcile-interval", time.Second, "request reconcile interval")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	config := scheduler.DefaultConfig(model.Resources{
		CPUMilli: *capacityCPU, MemoryBytes: *capacityMemoryMiB * model.MiB,
	})
	resourceScheduler, err := scheduler.New(config)
	if err != nil {
		logger.Error("invalid scheduler configuration", "error", err)
		os.Exit(2)
	}
	resourceController := controller.New(
		store.NewMemorySessionStore(), queue.New(), resourceScheduler, backend.NewMemoryBackend(),
	)

	server := &http.Server{
		Addr:              *listenAddress,
		Handler:           api.NewServer(resourceController, logger).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go reconcile(ctx, logger, resourceController, *reconcileInterval)
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()

	logger.Info("AgentRM control plane started", "listen", *listenAddress, "capacity", config.Capacity.String())
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped unexpectedly", "error", err)
		os.Exit(1)
	}
}

func reconcile(ctx context.Context, logger *slog.Logger, resourceController *controller.Controller, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			plan, processed, err := resourceController.ProcessNext(ctx)
			if err != nil {
				logger.Error("reconcile failed", "error", err)
				continue
			}
			if processed {
				logger.Info("resource request reconciled", "session_id", plan.Target.SessionID, "generation", plan.RequestGeneration, "waiting", plan.Waiting)
			}
		}
	}
}

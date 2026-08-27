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
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/cgroup"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/daemon"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/mlfq"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/store"
)

func main() {
	listenAddress := flag.String("listen", ":8080", "HTTP listen address")
	cgroupRoot := flag.String("cgroup-root", "/sys/fs/cgroup", "cgroup v2 mount root")
	sampleInterval := flag.Duration("sample-interval", 100*time.Millisecond, "cpu.stat sample interval")
	q0Weight := flag.Int("q0-weight", 10000, "Q0 cpu.weight")
	q1Weight := flag.Int("q1-weight", 3000, "Q1 cpu.weight")
	q2Weight := flag.Int("q2-weight", 500, "Q2 cpu.weight")
	q0Quantum := flag.Duration("q0-quantum", 250*time.Millisecond, "Q0 CPU service quantum")
	q1Quantum := flag.Duration("q1-quantum", 2*time.Second, "Q1 CPU service quantum")
	q1Aging := flag.Duration("q1-aging", 5*time.Second, "Q1 aging threshold")
	q2Aging := flag.Duration("q2-aging", 15*time.Second, "Q2 aging threshold")
	boostInterval := flag.Duration("boost-interval", 30*time.Second, "global priority boost interval")
	idleWeight := flag.Int("idle-weight", 100, "cpu.weight restored after a job finishes")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if *sampleInterval <= 0 {
		logger.Error("sample interval must be positive")
		os.Exit(2)
	}
	config := mlfq.Config{
		Q0:      mlfq.LevelConfig{Weight: *q0Weight, Quantum: *q0Quantum},
		Q1:      mlfq.LevelConfig{Weight: *q1Weight, Quantum: *q1Quantum},
		Q2:      mlfq.LevelConfig{Weight: *q2Weight},
		Q1Aging: *q1Aging, Q2Aging: *q2Aging, BoostInterval: *boostInterval, IdleWeight: *idleWeight,
	}
	engine, err := mlfq.New(config)
	if err != nil {
		logger.Error("invalid MLFQ configuration", "error", err)
		os.Exit(2)
	}
	cgroups, err := cgroup.NewFSClient(*cgroupRoot)
	if err != nil {
		logger.Error("invalid cgroup root", "error", err)
		os.Exit(2)
	}
	nodeDaemon := daemon.New(store.NewMemoryJobStore(), cgroups, engine)

	server := &http.Server{
		Addr:              *listenAddress,
		Handler:           api.NewServer(nodeDaemon, logger).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go reconcile(ctx, logger, nodeDaemon, *sampleInterval)
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()

	logger.Info("AgentRM node daemon started", "listen", *listenAddress, "cgroup_root", *cgroupRoot, "sample_interval", *sampleInterval)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped unexpectedly", "error", err)
		os.Exit(1)
	}
}

func reconcile(ctx context.Context, logger *slog.Logger, nodeDaemon *daemon.Daemon, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			report := nodeDaemon.Tick(ctx)
			if report.PriorityBoost {
				logger.Info("global MLFQ priority boost")
			}
			for _, result := range report.Jobs {
				if result.Error != "" {
					logger.Error("job reconcile failed", "job_id", result.JobID, "error", result.Error)
					continue
				}
				if result.Evaluation != nil && result.Evaluation.LevelChanged {
					logger.Info("job queue changed", "job_id", result.JobID, "from", result.Evaluation.PreviousLevel, "to", result.Evaluation.Job.Level, "reason", result.Evaluation.Reason, "cpu_weight", result.Evaluation.Job.CPUWeight)
				}
			}
		}
	}
}

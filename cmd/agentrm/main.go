package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/accounting"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/api"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/cgroup"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/discovery"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/mlfq"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/scheduler"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/store"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	listenAddress := flag.String("listen", ":8080", "HTTP listen address")
	nodeName := flag.String("node-name", os.Getenv("NODE_NAME"), "Kubernetes node managed by this daemon")
	kubeconfig := flag.String("kubeconfig", "", "optional kubeconfig path; defaults to in-cluster credentials")
	cgroupRoot := flag.String("cgroup-root", "/sys/fs/cgroup", "cgroup v2 mount root")
	bpfObject := flag.String("bpf-object", "/usr/lib/agentrm/agentrm.bpf.o", "compiled AgentRM eBPF object")
	q0Weight := flag.Int("q0-weight", 1000, "Q0 cpu.weight")
	q1Weight := flag.Int("q1-weight", 300, "Q1 cpu.weight")
	q2Weight := flag.Int("q2-weight", 100, "Q2 cpu.weight")
	q0Budget := flag.Duration("q0-budget", 4*time.Second, "Q0 CPU service credit")
	q1Budget := flag.Duration("q1-budget", 20*time.Second, "Q1 CPU service credit")
	boostInterval := flag.Duration("boost-interval", 60*time.Second, "global priority boost interval")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if *nodeName == "" {
		logger.Error("node name is required; use --node-name or NODE_NAME")
		os.Exit(2)
	}
	policy, err := mlfq.NewPolicy(mlfq.SessionConfig{
		Q0:            mlfq.SessionLevel{Weight: *q0Weight, Budget: *q0Budget},
		Q1:            mlfq.SessionLevel{Weight: *q1Weight, Budget: *q1Budget},
		Q2:            mlfq.SessionLevel{Weight: *q2Weight},
		BoostInterval: *boostInterval,
	})
	if err != nil {
		logger.Error("invalid MLFQ configuration", "error", err)
		os.Exit(2)
	}
	cgroups, err := cgroup.NewFSClient(*cgroupRoot)
	if err != nil {
		logger.Error("invalid cgroup root", "error", err)
		os.Exit(2)
	}
	resolver, err := cgroup.NewFSResolver(*cgroupRoot)
	if err != nil {
		logger.Error("create cgroup resolver", "error", err)
		os.Exit(2)
	}
	accountant, err := accounting.NewKernelSource(*bpfObject)
	if err != nil {
		logger.Error("start kernel CPU accounting", "error", err)
		os.Exit(1)
	}
	defer accountant.Close()
	kubeConfig, err := kubernetesConfig(*kubeconfig)
	if err != nil {
		logger.Error("create Kubernetes configuration", "error", err)
		os.Exit(1)
	}
	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		logger.Error("create Kubernetes client", "error", err)
		os.Exit(1)
	}
	podWatcher, err := discovery.NewWatcher(kubeClient, *nodeName, logger)
	if err != nil {
		logger.Error("create Agent Sandbox watcher", "error", err)
		os.Exit(1)
	}
	nodeScheduler, err := scheduler.NewController(
		store.NewMemorySandboxStore(), cgroups, resolver, accountant, policy, logger,
	)
	if err != nil {
		logger.Error("create node scheduler", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr: *listenAddress, Handler: api.NewServer(nodeScheduler, logger).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 3)
	go func() { errCh <- podWatcher.Run(ctx) }()
	go func() { errCh <- nodeScheduler.Run(ctx, podWatcher.Events()) }()
	go func() { errCh <- server.ListenAndServe() }()

	logger.Info("AgentRM node daemon started", "node", *nodeName, "listen", *listenAddress, "cgroup_root", *cgroupRoot, "bpf_object", *bpfObject)
	runErr := <-errCh
	stop()
	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownContext)
	if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, http.ErrServerClosed) {
		logger.Error("AgentRM stopped unexpectedly", "error", runErr)
		os.Exit(1)
	}
}

func kubernetesConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return rest.InClusterConfig()
}

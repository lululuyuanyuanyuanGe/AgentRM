package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/accounting"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/cgroup"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/discovery"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/mlfq"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/store"
	corev1 "k8s.io/api/core/v1"
)

type staticResolver struct{ location cgroup.Location }

func (r staticResolver) ResolvePod(context.Context, string) (cgroup.Location, error) {
	return r.location, nil
}

func TestControllerAdmitsBackingPodAndDemotesFromKernelEvents(t *testing.T) {
	groups := cgroup.NewMemoryClient()
	groups.Add("kubepods/pod-a", cgroup.CPUStat{}, 100)
	accountant := accounting.NewMemorySource()
	policy, _ := mlfq.NewPolicy(mlfq.DefaultSessionConfig())
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	controller, err := NewController(
		store.NewMemorySandboxStore(), groups, staticResolver{cgroup.Location{Path: "kubepods/pod-a", ID: 91}},
		accountant, policy, nil, WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	pod := discovery.SandboxPod{
		Type: discovery.EventUpsert, Namespace: "agents", SandboxName: "session-a", SandboxUID: "sandbox-a",
		PodName: "session-a", PodUID: "pod-a", NodeName: "worker-a", Phase: corev1.PodRunning,
	}
	if err := controller.HandlePod(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	assertWeight(t, groups, "kubepods/pod-a", 1000)
	config, ok := accountant.Configuration(91)
	if !ok || config.Level != accounting.LevelQ0 || config.BudgetNS != uint64(4*time.Second) {
		t.Fatalf("accounting config = %#v, %v", config, ok)
	}

	if err := accountant.Exhaust(91); err != nil {
		t.Fatal(err)
	}
	if err := controller.HandleThreshold(context.Background(), <-accountant.Events()); err != nil {
		t.Fatal(err)
	}
	assertWeight(t, groups, "kubepods/pod-a", 300)
	entity := controller.Sandboxes()[0]
	if entity.Level != model.QueueQ1 || entity.Generation != 2 {
		t.Fatalf("entity after Q0 event = %#v", entity)
	}

	if err := accountant.Exhaust(91); err != nil {
		t.Fatal(err)
	}
	if err := controller.HandleThreshold(context.Background(), <-accountant.Events()); err != nil {
		t.Fatal(err)
	}
	assertWeight(t, groups, "kubepods/pod-a", 100)
	if controller.Snapshot().Q2 != 1 {
		t.Fatalf("snapshot = %#v", controller.Snapshot())
	}
}

func TestControllerIgnoresStaleRingBufferEventAfterBoost(t *testing.T) {
	groups := cgroup.NewMemoryClient()
	groups.Add("pod-a", cgroup.CPUStat{}, 100)
	accountant := accounting.NewMemorySource()
	policy, _ := mlfq.NewPolicy(mlfq.DefaultSessionConfig())
	controller, _ := NewController(store.NewMemorySandboxStore(), groups, staticResolver{cgroup.Location{Path: "pod-a", ID: 7}}, accountant, policy, nil)
	pod := discovery.SandboxPod{Type: discovery.EventUpsert, Namespace: "n", SandboxName: "s", SandboxUID: "s1", PodName: "p", PodUID: "p1", NodeName: "node", Phase: corev1.PodRunning}
	_ = controller.HandlePod(context.Background(), pod)
	_ = accountant.Exhaust(7)
	stale := <-accountant.Events()
	_ = controller.HandleThreshold(context.Background(), stale)
	if err := controller.Boost(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.HandleThreshold(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	if controller.Snapshot().Q0 != 1 {
		t.Fatal("stale event demoted a newly boosted Sandbox")
	}
}

func TestControllerRemovesDeletedSandboxPod(t *testing.T) {
	groups := cgroup.NewMemoryClient()
	groups.Add("pod-a", cgroup.CPUStat{}, 100)
	accountant := accounting.NewMemorySource()
	policy, _ := mlfq.NewPolicy(mlfq.DefaultSessionConfig())
	controller, _ := NewController(store.NewMemorySandboxStore(), groups, staticResolver{cgroup.Location{Path: "pod-a", ID: 7}}, accountant, policy, nil)
	pod := discovery.SandboxPod{Type: discovery.EventUpsert, Namespace: "n", SandboxName: "s", SandboxUID: "s1", PodName: "p", PodUID: "p1", NodeName: "node", Phase: corev1.PodRunning}
	_ = controller.HandlePod(context.Background(), pod)
	pod.Type = discovery.EventDelete
	if err := controller.HandlePod(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	if controller.Snapshot().Sandboxes != 0 {
		t.Fatal("deleted Pod remained scheduled")
	}
	if _, ok := accountant.Configuration(7); ok {
		t.Fatal("deleted Pod remained in accounting map")
	}
}

func assertWeight(t *testing.T, groups *cgroup.MemoryClient, path string, want int) {
	t.Helper()
	weight, err := groups.ReadWeight(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if weight != want {
		t.Fatalf("cpu.weight = %d, want %d", weight, want)
	}
}

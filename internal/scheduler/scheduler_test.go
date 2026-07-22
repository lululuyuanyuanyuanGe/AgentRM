package scheduler

import (
	"testing"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
)

func TestPlanUsesFreeCapacityThenReclaimsLowestClass(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := mustScheduler(t, model.Resources{CPUMilli: 12000, MemoryBytes: 24 * 1024 * model.MiB}, now)
	sessions := []model.Session{
		session("target", model.SessionRunningTool, 1000, 8000, 2000, now),
		session("waiting", model.SessionWaitingUser, 1000, 8000, 6000, now.Add(-time.Minute)),
		session("active", model.SessionRunningTool, 1000, 8000, 3000, now),
	}

	plan, err := s.Plan(sessions, model.ResourceRequest{
		SessionID: "target", Generation: 3, Priority: model.PriorityInteractive,
		Desired: model.Resources{CPUMilli: 8000, MemoryBytes: 1024 * model.MiB},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Target.After.CPUMilli != 8000 || plan.Waiting {
		t.Fatalf("unexpected target plan: %+v", plan)
	}
	if len(plan.Victims) != 1 || plan.Victims[0].SessionID != "waiting" || plan.Victims[0].After.CPUMilli != 1000 {
		t.Fatalf("unexpected victim plan: %+v", plan.Victims)
	}
}

func TestPlanReclaimsOnlyWhatTargetNeeds(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := mustScheduler(t, model.Resources{CPUMilli: 8000, MemoryBytes: 16 * 1024 * model.MiB}, now)
	sessions := []model.Session{
		session("target", model.SessionRunningTool, 1000, 8000, 3000, now),
		session("victim", model.SessionWaitingUser, 1000, 8000, 5000, now.Add(-time.Minute)),
	}
	plan, err := s.Plan(sessions, model.ResourceRequest{SessionID: "target", Generation: 1, Priority: model.PriorityInteractive, Desired: model.Resources{CPUMilli: 5000, MemoryBytes: 1024 * model.MiB}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Victims[0].After.CPUMilli != 3000 {
		t.Fatalf("victim after = %d, want 3000", plan.Victims[0].After.CPUMilli)
	}
}

func TestPlanWaitsWhenAllSessionsAtMinimum(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := mustScheduler(t, model.Resources{CPUMilli: 4000, MemoryBytes: 8 * 1024 * model.MiB}, now)
	target := session("target", model.SessionRunningTool, 2000, 8000, 2000, now)
	victim := session("victim", model.SessionRunningTool, 2000, 8000, 2000, now)
	plan, err := s.Plan([]model.Session{target, victim}, model.ResourceRequest{SessionID: "target", Generation: 1, Priority: model.PriorityInteractive, Desired: model.Resources{CPUMilli: 8000, MemoryBytes: 1024 * model.MiB}})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Waiting || plan.Shortfall.CPUMilli != 6000 || plan.Target.After.CPUMilli != 2000 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestMemoryReclamationRequiresStableHeadroom(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := mustScheduler(t, model.Resources{CPUMilli: 8000, MemoryBytes: 8 * 1024 * model.MiB}, now)
	target := session("target", model.SessionRunningTool, 1000, 8000, 1000, now)
	target.Allocated.MemoryBytes = 2 * 1024 * model.MiB
	target.Min.MemoryBytes = 1024 * model.MiB
	target.Max.MemoryBytes = 8 * 1024 * model.MiB
	victim := session("victim", model.SessionWaitingUser, 1000, 8000, 1000, now)
	victim.Allocated.MemoryBytes = 6 * 1024 * model.MiB
	victim.Min.MemoryBytes = 1024 * model.MiB
	victim.Max.MemoryBytes = 8 * 1024 * model.MiB
	victim.MemoryWorkingSet = 2 * 1024 * model.MiB

	request := model.ResourceRequest{SessionID: "target", Generation: 1, Priority: model.PriorityInteractive, Desired: model.Resources{CPUMilli: 1000, MemoryBytes: 4 * 1024 * model.MiB}}
	plan, err := s.Plan([]model.Session{target, victim}, request)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Waiting || plan.Target.After.MemoryBytes != target.Allocated.MemoryBytes {
		t.Fatalf("unstable memory should not be reclaimed: %+v", plan)
	}

	victim.MemoryStableSince = now.Add(-3 * time.Minute)
	plan, err = s.Plan([]model.Session{target, victim}, request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Waiting || plan.Target.After.MemoryBytes != request.Desired.MemoryBytes {
		t.Fatalf("stable memory should be reclaimed: %+v", plan)
	}
}

func TestUnsafeTargetMemoryShrinkRemainsDeferred(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := mustScheduler(t, model.Resources{CPUMilli: 8000, MemoryBytes: 16 * 1024 * model.MiB}, now)
	target := session("target", model.SessionRunningTool, 1000, 8000, 2000, now)
	target.Allocated.MemoryBytes = 4 * 1024 * model.MiB
	target.Desired.MemoryBytes = target.Allocated.MemoryBytes
	target.Max.MemoryBytes = 8 * 1024 * model.MiB

	plan, err := s.Plan([]model.Session{target}, model.ResourceRequest{
		SessionID: "target", Generation: 2, Priority: model.PriorityInteractive,
		Desired: model.Resources{CPUMilli: 2000, MemoryBytes: 2 * 1024 * model.MiB},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Deferred || plan.Waiting || plan.Target.After.MemoryBytes != target.Allocated.MemoryBytes {
		t.Fatalf("unexpected unsafe shrink plan: %+v", plan)
	}
}

func mustScheduler(t *testing.T, capacity model.Resources, now time.Time) *Scheduler {
	t.Helper()
	s, err := New(DefaultConfig(capacity))
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	return s
}

func session(id string, state model.SessionState, minCPU, maxCPU, allocatedCPU int64, lastActive time.Time) model.Session {
	return model.Session{
		ID:        id,
		Min:       model.Resources{CPUMilli: minCPU, MemoryBytes: 1024 * model.MiB},
		Max:       model.Resources{CPUMilli: maxCPU, MemoryBytes: 8 * 1024 * model.MiB},
		Desired:   model.Resources{CPUMilli: allocatedCPU, MemoryBytes: 1024 * model.MiB},
		Allocated: model.Resources{CPUMilli: allocatedCPU, MemoryBytes: 1024 * model.MiB},
		State:     state, Priority: model.PriorityNormal, LastActiveAt: lastActive,
	}
}

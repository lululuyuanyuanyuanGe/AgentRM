package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/backend"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/queue"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/scheduler"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/store"
)

func TestControllerAllocatesAndRejectsStaleGeneration(t *testing.T) {
	controller, _ := newController(t, model.Resources{CPUMilli: 8000, MemoryBytes: 16 * 1024 * model.MiB})
	ctx := context.Background()
	created, err := controller.CreateSession(ctx, newSession("a"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Allocated.CPUMilli != 1000 {
		t.Fatalf("allocated = %d, want 1000", created.Allocated.CPUMilli)
	}

	_, err = controller.RequestResources(model.ResourceRequest{SessionID: "a", Generation: 2, Priority: model.PriorityInteractive, Desired: model.Resources{CPUMilli: 4000, MemoryBytes: 2 * 1024 * model.MiB}})
	if err != nil {
		t.Fatal(err)
	}
	if _, processed, err := controller.ProcessNext(ctx); err != nil || !processed {
		t.Fatalf("process next: processed=%v err=%v", processed, err)
	}
	updated, _ := controller.GetSession("a")
	if updated.Allocated.CPUMilli != 4000 || updated.AppliedGeneration != 2 {
		t.Fatalf("unexpected session: %+v", updated)
	}

	_, err = controller.RequestResources(model.ResourceRequest{SessionID: "a", Generation: 1, Priority: model.PriorityNormal, Desired: model.Resources{CPUMilli: 2000, MemoryBytes: 2 * 1024 * model.MiB}})
	if !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("err = %v, want ErrStaleGeneration", err)
	}
}

func TestControllerReclaimsAndSuspends(t *testing.T) {
	controller, memoryBackend := newController(t, model.Resources{CPUMilli: 8000, MemoryBytes: 16 * 1024 * model.MiB})
	ctx := context.Background()
	for _, id := range []string{"target", "victim"} {
		if _, err := controller.CreateSession(ctx, newSession(id)); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = controller.UpdateState("target", model.SessionRunningTool, model.PriorityInteractive)
	_, _ = controller.UpdateState("victim", model.SessionWaitingUser, model.PriorityNormal)
	_, _ = controller.RequestResources(model.ResourceRequest{SessionID: "victim", Generation: 1, Priority: model.PriorityNormal, Desired: model.Resources{CPUMilli: 6000, MemoryBytes: 1024 * model.MiB}})
	_, _, _ = controller.ProcessNext(ctx)
	_, _ = controller.RequestResources(model.ResourceRequest{SessionID: "target", Generation: 1, Priority: model.PriorityInteractive, Desired: model.Resources{CPUMilli: 6000, MemoryBytes: 1024 * model.MiB}})
	plan, _, err := controller.ProcessNext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Victims) != 1 || plan.Victims[0].SessionID != "victim" {
		t.Fatalf("unexpected plan: %+v", plan)
	}

	suspended, err := controller.SuspendSession(ctx, "victim")
	if err != nil {
		t.Fatal(err)
	}
	if suspended.State != model.SessionSuspended || suspended.Allocated != (model.Resources{}) || suspended.CheckpointReference == "" {
		t.Fatalf("unexpected suspended state: %+v", suspended)
	}
	if resources, ok := memoryBackend.Resources("victim"); !ok || !resources.IsZero() {
		t.Fatalf("backend resources after suspend: %+v, ok=%v", resources, ok)
	}

	resumed, err := controller.ResumeSession(ctx, "victim")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != model.SessionReady || resumed.Allocated != resumed.Min {
		t.Fatalf("unexpected resumed state: %+v", resumed)
	}
}

func newController(t *testing.T, capacity model.Resources) (*Controller, *backend.MemoryBackend) {
	t.Helper()
	resourceScheduler, err := scheduler.New(scheduler.DefaultConfig(capacity))
	if err != nil {
		t.Fatal(err)
	}
	memoryBackend := backend.NewMemoryBackend()
	return New(store.NewMemorySessionStore(), queue.New(), resourceScheduler, memoryBackend), memoryBackend
}

func newSession(id string) model.Session {
	return model.Session{
		ID:           id,
		Min:          model.Resources{CPUMilli: 1000, MemoryBytes: 1024 * model.MiB},
		Max:          model.Resources{CPUMilli: 8000, MemoryBytes: 8 * 1024 * model.MiB},
		Priority:     model.PriorityNormal,
		LastActiveAt: time.Now().UTC(),
	}
}

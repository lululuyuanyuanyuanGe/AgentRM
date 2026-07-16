package queue

import (
	"testing"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
)

func TestQueueCoalescesBySessionAndGeneration(t *testing.T) {
	q := New()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	q.Enqueue(model.ResourceRequest{SessionID: "a", Generation: 1, Desired: model.Resources{CPUMilli: 8000}, Priority: model.PriorityNormal, CreatedAt: base})
	q.Enqueue(model.ResourceRequest{SessionID: "a", Generation: 2, Desired: model.Resources{CPUMilli: 4000}, Priority: model.PriorityNormal, CreatedAt: base.Add(time.Second)})
	if accepted := q.Enqueue(model.ResourceRequest{SessionID: "a", Generation: 1, Desired: model.Resources{CPUMilli: 1000}, Priority: model.PriorityNormal}); accepted {
		t.Fatal("stale generation was accepted")
	}

	if q.Len() != 1 {
		t.Fatalf("queue length = %d, want 1", q.Len())
	}
	request, ok := q.Pop()
	if !ok || request.Generation != 2 || request.Desired.CPUMilli != 4000 {
		t.Fatalf("unexpected request: %+v", request)
	}
}

func TestQueueOrdersByPriorityThenAge(t *testing.T) {
	q := New()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q.Enqueue(model.ResourceRequest{SessionID: "background", Generation: 1, Priority: model.PriorityBackground, CreatedAt: base})
	q.Enqueue(model.ResourceRequest{SessionID: "new-interactive", Generation: 1, Priority: model.PriorityInteractive, CreatedAt: base.Add(time.Second)})
	q.Enqueue(model.ResourceRequest{SessionID: "old-interactive", Generation: 1, Priority: model.PriorityInteractive, CreatedAt: base})

	for index, want := range []string{"old-interactive", "new-interactive", "background"} {
		got, ok := q.Pop()
		if !ok || got.SessionID != want {
			t.Fatalf("pop %d = %q, want %q", index, got.SessionID, want)
		}
	}
}

func TestQueueSkipsTemporarilyDeferredHighPriorityRequest(t *testing.T) {
	q := New()
	q.Enqueue(model.ResourceRequest{
		SessionID: "deferred", Generation: 1, Priority: model.PriorityInteractive,
		NotBefore: time.Now().UTC().Add(time.Minute),
	})
	q.Enqueue(model.ResourceRequest{SessionID: "ready", Generation: 1, Priority: model.PriorityBackground})

	request, ok := q.Pop()
	if !ok || request.SessionID != "ready" {
		t.Fatalf("pop = %+v, ok=%v; want ready request", request, ok)
	}
	if q.Len() != 1 {
		t.Fatalf("queue length = %d, want deferred request to remain", q.Len())
	}
}

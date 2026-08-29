package mlfq

import (
	"testing"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
)

func TestSandboxCreditFollowsSessionAcrossDemotions(t *testing.T) {
	policy, err := NewPolicy(DefaultSessionConfig())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	entity, err := policy.NewSandbox(validSandbox(), now)
	if err != nil {
		t.Fatal(err)
	}
	if entity.Level != model.QueueQ0 || entity.BudgetNS != uint64(4*time.Second) {
		t.Fatalf("new sandbox = %s/%d, want Q0/4s", entity.Level, entity.BudgetNS)
	}

	entity, err = policy.Demote(entity, uint64(4*time.Second), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if entity.Level != model.QueueQ1 || entity.CPUWeight != 300 || entity.Generation != 2 {
		t.Fatalf("first demotion = %#v", entity)
	}

	entity, err = policy.Demote(entity, uint64(20*time.Second), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if entity.Level != model.QueueQ2 || entity.BudgetNS != 0 || entity.AccountedNS != uint64(24*time.Second) {
		t.Fatalf("second demotion = %#v", entity)
	}
}

func TestPriorityBoostReturnsSessionToQ0(t *testing.T) {
	policy, _ := NewPolicy(DefaultSessionConfig())
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	entity, _ := policy.NewSandbox(validSandbox(), now)
	entity, _ = policy.Demote(entity, entity.BudgetNS, now.Add(time.Second))
	entity, err := policy.Boost(entity, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if entity.Level != model.QueueQ0 || entity.Promotions != 1 || entity.Generation != 3 {
		t.Fatalf("boosted sandbox = %#v", entity)
	}
}

func TestDemotionRejectsEarlyEvent(t *testing.T) {
	policy, _ := NewPolicy(DefaultSessionConfig())
	entity, _ := policy.NewSandbox(validSandbox(), time.Now())
	if _, err := policy.Demote(entity, entity.BudgetNS-1, time.Now()); err == nil {
		t.Fatal("expected an event below budget to be rejected")
	}
}

func validSandbox() model.SandboxEntity {
	return model.SandboxEntity{
		Namespace: "agents", SandboxName: "session-a", SandboxUID: "sandbox-uid",
		PodName: "session-a", PodUID: "pod-uid", NodeName: "worker-a",
		CgroupPath: "kubepods.slice/pod.slice", CgroupID: 42,
	}
}

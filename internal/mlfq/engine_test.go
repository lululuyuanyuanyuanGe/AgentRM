package mlfq

import (
	"testing"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
)

func TestNewJobStartsInQ0(t *testing.T) {
	engine := mustEngine(t)
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	job, err := engine.NewJob("job-a", "sandbox-a", "kubepods/job-a", 120, now)
	if err != nil {
		t.Fatal(err)
	}
	if job.Level != model.QueueQ0 || job.CPUWeight != 10000 || job.CPUUsageUsec != 120 {
		t.Fatalf("unexpected new job: %+v", job)
	}
}

func TestQ0DemotionUsesCPUServiceNotWallTime(t *testing.T) {
	engine := mustEngine(t)
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	job, _ := engine.NewJob("job-a", "sandbox-a", "job-a", 100, now)

	first, err := engine.Evaluate(job, 299, now.Add(10*time.Second), false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Job.Level != model.QueueQ0 {
		t.Fatalf("wall time alone demoted job: %+v", first)
	}

	second, err := engine.Evaluate(first.Job, 300, now.Add(11*time.Second), false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Job.Level != model.QueueQ1 || second.Reason != ReasonQuantum || second.Job.CPUWeight != 3000 {
		t.Fatalf("unexpected Q0 demotion: %+v", second)
	}
}

func TestQ1DemotesAndQ2KeepsRunning(t *testing.T) {
	engine := mustEngine(t)
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	job, _ := engine.NewJob("job-a", "sandbox-a", "job-a", 0, now)
	job.Level = model.QueueQ1
	job.CPUWeight = engine.Weight(model.QueueQ1)
	job.LevelEnteredAt = now

	demoted, err := engine.Evaluate(job, 1000, now.Add(time.Second), false)
	if err != nil {
		t.Fatal(err)
	}
	if demoted.Job.Level != model.QueueQ2 || demoted.Job.Demotions != 1 {
		t.Fatalf("unexpected Q1 demotion: %+v", demoted)
	}

	continued, err := engine.Evaluate(demoted.Job, 10000, now.Add(2*time.Second), false)
	if err != nil {
		t.Fatal(err)
	}
	if continued.Job.Level != model.QueueQ2 || continued.LevelChanged {
		t.Fatalf("Q2 should continue at its current level: %+v", continued)
	}
}

func TestAgingPromotesOneLevelAtATime(t *testing.T) {
	engine := mustEngine(t)
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	job, _ := engine.NewJob("job-a", "sandbox-a", "job-a", 0, now)
	job.Level = model.QueueQ2
	job.CPUWeight = engine.Weight(model.QueueQ2)
	job.LevelEnteredAt = now

	q1, err := engine.Evaluate(job, 0, now.Add(4*time.Second), false)
	if err != nil {
		t.Fatal(err)
	}
	if q1.Job.Level != model.QueueQ1 || q1.Reason != ReasonAging {
		t.Fatalf("unexpected Q2 aging result: %+v", q1)
	}

	q0, err := engine.Evaluate(q1.Job, 0, q1.Job.LevelEnteredAt.Add(2*time.Second), false)
	if err != nil {
		t.Fatal(err)
	}
	if q0.Job.Level != model.QueueQ0 || q0.Job.Promotions != 2 {
		t.Fatalf("unexpected Q1 aging result: %+v", q0)
	}
}

func TestPeriodicBoostReturnsLongJobToQ0(t *testing.T) {
	engine := mustEngine(t)
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	if engine.BeginTick(now) {
		t.Fatal("first tick must initialize, not boost")
	}
	if engine.BeginTick(now.Add(9 * time.Second)) {
		t.Fatal("boost fired too early")
	}
	boost := engine.BeginTick(now.Add(10 * time.Second))
	if !boost {
		t.Fatal("expected priority boost")
	}

	job, _ := engine.NewJob("job-a", "sandbox-a", "job-a", 0, now)
	job.Level = model.QueueQ2
	job.CPUWeight = engine.Weight(model.QueueQ2)
	result, err := engine.Evaluate(job, 500, now.Add(10*time.Second), boost)
	if err != nil {
		t.Fatal(err)
	}
	if result.Job.Level != model.QueueQ0 || result.Reason != ReasonPriorityBoost {
		t.Fatalf("unexpected boost result: %+v", result)
	}
}

func TestCPUCounterResetDoesNotCreateFakeService(t *testing.T) {
	engine := mustEngine(t)
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	job, _ := engine.NewJob("job-a", "sandbox-a", "job-a", 900, now)
	result, err := engine.Evaluate(job, 10, now.Add(time.Second), false)
	if err != nil {
		t.Fatal(err)
	}
	if result.ServiceDeltaUsec != 0 || result.Job.ServiceInLevelUsec != 0 || result.Reason != ReasonCounterReset {
		t.Fatalf("unexpected reset handling: %+v", result)
	}
}

func mustEngine(t *testing.T) *Engine {
	t.Helper()
	config := DefaultConfig()
	config.Q0.Quantum = 200 * time.Microsecond
	config.Q1.Quantum = time.Millisecond
	config.Q1Aging = 2 * time.Second
	config.Q2Aging = 4 * time.Second
	config.BoostInterval = 10 * time.Second
	engine, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/cgroup"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/mlfq"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/store"
)

func TestRegisterJobStartsAtQ0AndIsIdempotent(t *testing.T) {
	daemon, groups, _ := testDaemon(t)
	groups.Add("sandbox-a/job-a", cgroup.CPUStat{UsageUsec: 42}, 100)

	job, err := daemon.RegisterJob(context.Background(), "job-a", "sandbox-a", "sandbox-a/job-a")
	if err != nil {
		t.Fatal(err)
	}
	if job.Level != model.QueueQ0 || job.CPUUsageUsec != 42 || job.CPUWeight != 10000 {
		t.Fatalf("unexpected registered job: %+v", job)
	}
	weight, _ := groups.ReadWeight(context.Background(), job.CgroupPath)
	if weight != 10000 {
		t.Fatalf("initial weight = %d, want 10000", weight)
	}

	again, err := daemon.RegisterJob(context.Background(), "job-a", "sandbox-a", "sandbox-a/job-a")
	if err != nil || again.ID != job.ID {
		t.Fatalf("idempotent registration failed: job=%+v err=%v", again, err)
	}
}

func TestLongJobDemotesWhileNewShortJobStaysQ0(t *testing.T) {
	daemon, groups, clock := testDaemon(t)
	groups.Add("sandbox-long/job-long", cgroup.CPUStat{}, 100)
	if _, err := daemon.RegisterJob(context.Background(), "long", "sandbox-long", "sandbox-long/job-long"); err != nil {
		t.Fatal(err)
	}

	_ = groups.SetUsage("sandbox-long/job-long", 250)
	*clock = clock.Add(time.Second)
	daemon.Tick(context.Background())
	longJob, _ := daemon.GetJob("long")
	if longJob.Level != model.QueueQ1 || longJob.CPUWeight != 3000 {
		t.Fatalf("long job did not enter Q1: %+v", longJob)
	}

	_ = groups.SetUsage("sandbox-long/job-long", 2250)
	*clock = clock.Add(time.Second)
	daemon.Tick(context.Background())
	longJob, _ = daemon.GetJob("long")
	if longJob.Level != model.QueueQ2 || longJob.CPUWeight != 500 {
		t.Fatalf("long job did not enter Q2: %+v", longJob)
	}

	groups.Add("sandbox-short/job-short", cgroup.CPUStat{}, 100)
	shortJob, err := daemon.RegisterJob(context.Background(), "short", "sandbox-short", "sandbox-short/job-short")
	if err != nil {
		t.Fatal(err)
	}
	if shortJob.Level != model.QueueQ0 || shortJob.CPUWeight != 10000 {
		t.Fatalf("new short job did not start in Q0: %+v", shortJob)
	}
	longWeight, _ := groups.ReadWeight(context.Background(), longJob.CgroupPath)
	shortWeight, _ := groups.ReadWeight(context.Background(), shortJob.CgroupPath)
	if longWeight != 500 || shortWeight != 10000 {
		t.Fatalf("unexpected relative weights: long=%d short=%d", longWeight, shortWeight)
	}

	snapshot := daemon.Snapshot()
	if snapshot.Q0 != 1 || snapshot.Q2 != 1 || snapshot.Running != 2 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

func TestFinishRestoresIdleWeight(t *testing.T) {
	daemon, groups, clock := testDaemon(t)
	groups.Add("sandbox-a/job-a", cgroup.CPUStat{}, 100)
	_, _ = daemon.RegisterJob(context.Background(), "job-a", "sandbox-a", "sandbox-a/job-a")
	*clock = clock.Add(time.Second)

	job, err := daemon.FinishJob(context.Background(), "job-a", model.JobFinished)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != model.JobFinished || job.FinishedAt == nil || job.CPUWeight != 100 {
		t.Fatalf("unexpected finished job: %+v", job)
	}
	weight, _ := groups.ReadWeight(context.Background(), job.CgroupPath)
	if weight != 100 {
		t.Fatalf("finished cgroup weight = %d, want 100", weight)
	}
}

func TestOneActiveJobPerCgroup(t *testing.T) {
	daemon, groups, _ := testDaemon(t)
	groups.Add("sandbox-a/job", cgroup.CPUStat{}, 100)
	_, _ = daemon.RegisterJob(context.Background(), "job-a", "sandbox-a", "sandbox-a/job")
	_, err := daemon.RegisterJob(context.Background(), "job-b", "sandbox-a", "sandbox-a/job")
	if !errors.Is(err, ErrCgroupBusy) {
		t.Fatalf("err=%v, want ErrCgroupBusy", err)
	}
}

func testDaemon(t *testing.T) (*Daemon, *cgroup.MemoryClient, *time.Time) {
	t.Helper()
	config := mlfq.DefaultConfig()
	config.Q0.Quantum = 250 * time.Microsecond
	config.Q1.Quantum = 2 * time.Millisecond
	config.Q1Aging = 5 * time.Second
	config.Q2Aging = 15 * time.Second
	config.BoostInterval = 30 * time.Second
	engine, err := mlfq.New(config)
	if err != nil {
		t.Fatal(err)
	}
	groups := cgroup.NewMemoryClient()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	daemon := New(store.NewMemoryJobStore(), groups, engine)
	daemon.now = func() time.Time { return now }
	return daemon, groups, &now
}

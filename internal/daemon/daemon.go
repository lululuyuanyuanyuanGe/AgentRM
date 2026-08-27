package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/cgroup"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/mlfq"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/store"
)

var ErrCgroupBusy = errors.New("cgroup already has a running job")

type JobTickResult struct {
	JobID      string           `json:"job_id"`
	Evaluation *mlfq.Evaluation `json:"evaluation,omitempty"`
	Applied    bool             `json:"weight_applied"`
	Error      string           `json:"error,omitempty"`
}

type TickReport struct {
	ObservedAt    time.Time       `json:"observed_at"`
	PriorityBoost bool            `json:"priority_boost"`
	Jobs          []JobTickResult `json:"jobs"`
}

type Snapshot struct {
	Running  int `json:"running"`
	Finished int `json:"finished"`
	Failed   int `json:"failed"`
	Q0       int `json:"q0"`
	Q1       int `json:"q1"`
	Q2       int `json:"q2"`
}

type Daemon struct {
	store   store.JobStore
	cgroups cgroup.Client
	engine  *mlfq.Engine
	now     func() time.Time
	mu      sync.Mutex
}

func New(jobStore store.JobStore, cgroups cgroup.Client, engine *mlfq.Engine) *Daemon {
	return &Daemon{
		store: jobStore, cgroups: cgroups, engine: engine,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (d *Daemon) Config() mlfq.Config { return d.engine.Config() }

func (d *Daemon) RegisterJob(ctx context.Context, jobID, sandboxID, cgroupPath string) (model.ToolJob, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if existing, err := d.store.Get(jobID); err == nil {
		if existing.Active() && existing.SandboxID == sandboxID && existing.CgroupPath == cgroupPath {
			return existing, nil
		}
		return model.ToolJob{}, store.ErrJobExists
	} else if !errors.Is(err, store.ErrJobNotFound) {
		return model.ToolJob{}, err
	}
	for _, existing := range d.store.List() {
		if existing.Active() && existing.CgroupPath == cgroupPath {
			return model.ToolJob{}, ErrCgroupBusy
		}
	}

	stat, err := d.cgroups.ReadCPUStat(ctx, cgroupPath)
	if err != nil {
		return model.ToolJob{}, fmt.Errorf("read initial cpu.stat: %w", err)
	}
	job, err := d.engine.NewJob(jobID, sandboxID, cgroupPath, stat.UsageUsec, d.now())
	if err != nil {
		return model.ToolJob{}, err
	}
	if err := d.cgroups.WriteWeight(ctx, cgroupPath, job.CPUWeight); err != nil {
		return model.ToolJob{}, fmt.Errorf("set initial Q0 weight: %w", err)
	}
	if err := d.store.Create(job); err != nil {
		_ = d.cgroups.WriteWeight(ctx, cgroupPath, d.engine.Config().IdleWeight)
		return model.ToolJob{}, err
	}
	return job, nil
}

func (d *Daemon) FinishJob(ctx context.Context, jobID string, finalState model.JobState) (model.ToolJob, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if finalState != model.JobFinished && finalState != model.JobFailed {
		return model.ToolJob{}, errors.New("final state must be FINISHED or FAILED")
	}
	job, err := d.store.Get(jobID)
	if err != nil {
		return model.ToolJob{}, err
	}
	if !job.Active() {
		return job, nil
	}
	if err := d.cgroups.WriteWeight(ctx, job.CgroupPath, d.engine.Config().IdleWeight); err != nil {
		return model.ToolJob{}, fmt.Errorf("restore idle cpu.weight: %w", err)
	}
	finishedAt := d.now()
	return d.store.Update(jobID, func(current *model.ToolJob) error {
		current.State = finalState
		current.CPUWeight = d.engine.Config().IdleWeight
		current.FinishedAt = &finishedAt
		return nil
	})
}

func (d *Daemon) Tick(ctx context.Context) TickReport {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.now()
	boost := d.engine.BeginTick(now)
	report := TickReport{ObservedAt: now, PriorityBoost: boost}
	for _, job := range d.store.List() {
		if !job.Active() {
			continue
		}
		result := JobTickResult{JobID: job.ID}
		stat, err := d.cgroups.ReadCPUStat(ctx, job.CgroupPath)
		if err != nil {
			result.Error = fmt.Sprintf("read cpu.stat: %v", err)
			report.Jobs = append(report.Jobs, result)
			continue
		}
		evaluation, err := d.engine.Evaluate(job, stat.UsageUsec, now, boost)
		if err != nil {
			result.Error = err.Error()
			report.Jobs = append(report.Jobs, result)
			continue
		}
		result.Evaluation = &evaluation
		if _, err := d.store.Update(job.ID, func(current *model.ToolJob) error {
			*current = evaluation.Job
			return nil
		}); err != nil {
			result.Error = fmt.Sprintf("update job: %v", err)
			report.Jobs = append(report.Jobs, result)
			continue
		}
		currentWeight, err := d.cgroups.ReadWeight(ctx, job.CgroupPath)
		if err != nil {
			result.Error = fmt.Sprintf("read cpu.weight: %v", err)
			report.Jobs = append(report.Jobs, result)
			continue
		}
		if currentWeight != evaluation.Job.CPUWeight {
			if err := d.cgroups.WriteWeight(ctx, job.CgroupPath, evaluation.Job.CPUWeight); err != nil {
				result.Error = fmt.Sprintf("write cpu.weight: %v", err)
				report.Jobs = append(report.Jobs, result)
				continue
			}
		}
		result.Applied = true
		report.Jobs = append(report.Jobs, result)
	}
	return report
}

func (d *Daemon) GetJob(jobID string) (model.ToolJob, error) { return d.store.Get(jobID) }

func (d *Daemon) ListJobs() []model.ToolJob { return d.store.List() }

func (d *Daemon) Snapshot() Snapshot {
	var snapshot Snapshot
	for _, job := range d.store.List() {
		switch job.State {
		case model.JobRunning:
			snapshot.Running++
			switch job.Level {
			case model.QueueQ0:
				snapshot.Q0++
			case model.QueueQ1:
				snapshot.Q1++
			case model.QueueQ2:
				snapshot.Q2++
			}
		case model.JobFinished:
			snapshot.Finished++
		case model.JobFailed:
			snapshot.Failed++
		}
	}
	return snapshot
}

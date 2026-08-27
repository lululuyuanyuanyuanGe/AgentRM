package mlfq

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
)

type LevelConfig struct {
	Weight  int
	Quantum time.Duration
}

type Config struct {
	Q0            LevelConfig
	Q1            LevelConfig
	Q2            LevelConfig
	Q1Aging       time.Duration
	Q2Aging       time.Duration
	BoostInterval time.Duration
	IdleWeight    int
}

func DefaultConfig() Config {
	return Config{
		Q0:            LevelConfig{Weight: 10000, Quantum: 250 * time.Millisecond},
		Q1:            LevelConfig{Weight: 3000, Quantum: 2 * time.Second},
		Q2:            LevelConfig{Weight: 500},
		Q1Aging:       5 * time.Second,
		Q2Aging:       15 * time.Second,
		BoostInterval: 30 * time.Second,
		IdleWeight:    100,
	}
}

func (c Config) Validate() error {
	for level, weight := range map[string]int{"Q0": c.Q0.Weight, "Q1": c.Q1.Weight, "Q2": c.Q2.Weight, "idle": c.IdleWeight} {
		if weight < 1 || weight > 10000 {
			return fmt.Errorf("%s weight must be between 1 and 10000", level)
		}
	}
	if !(c.Q0.Weight > c.Q1.Weight && c.Q1.Weight > c.Q2.Weight) {
		return errors.New("queue weights must satisfy Q0 > Q1 > Q2")
	}
	if c.Q0.Quantum <= 0 || c.Q1.Quantum <= 0 {
		return errors.New("Q0 and Q1 quantum must be positive")
	}
	if c.Q1Aging <= 0 || c.Q2Aging <= 0 || c.BoostInterval <= 0 {
		return errors.New("aging and boost durations must be positive")
	}
	return nil
}

type Reason string

const (
	ReasonNone          Reason = "none"
	ReasonQuantum       Reason = "quantum_exhausted"
	ReasonAging         Reason = "aging"
	ReasonPriorityBoost Reason = "priority_boost"
	ReasonCounterReset  Reason = "cpu_counter_reset"
)

type Evaluation struct {
	Job              model.ToolJob    `json:"job"`
	PreviousLevel    model.QueueLevel `json:"previous_queue"`
	Reason           Reason           `json:"reason"`
	ServiceDeltaUsec uint64           `json:"service_delta_usec"`
	LevelChanged     bool             `json:"level_changed"`
	WeightChanged    bool             `json:"weight_changed"`
}

type Engine struct {
	config    Config
	mu        sync.Mutex
	lastBoost time.Time
}

func New(config Config) (*Engine, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Engine{config: config}, nil
}

func (e *Engine) Config() Config { return e.config }

func (e *Engine) NewJob(jobID, sandboxID, cgroupPath string, initialUsageUsec uint64, now time.Time) (model.ToolJob, error) {
	job := model.ToolJob{
		ID: jobID, SandboxID: sandboxID, CgroupPath: cgroupPath,
		State: model.JobRunning, Level: model.QueueQ0, CPUWeight: e.config.Q0.Weight,
		CPUUsageUsec: initialUsageUsec, StartedAt: now, LevelEnteredAt: now, LastObservedAt: now,
	}
	return job, job.Validate()
}

// BeginTick returns true when every active job should be moved back to Q0.
func (e *Engine) BeginTick(now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lastBoost.IsZero() {
		e.lastBoost = now
		return false
	}
	if now.Sub(e.lastBoost) < e.config.BoostInterval {
		return false
	}
	e.lastBoost = now
	return true
}

func (e *Engine) Evaluate(job model.ToolJob, usageUsec uint64, now time.Time, globalBoost bool) (Evaluation, error) {
	if err := job.Validate(); err != nil {
		return Evaluation{}, err
	}
	if !job.Active() {
		return Evaluation{Job: job, PreviousLevel: job.Level, Reason: ReasonNone}, nil
	}

	previousLevel := job.Level
	previousWeight := job.CPUWeight
	reason := ReasonNone
	var delta uint64
	if usageUsec < job.CPUUsageUsec {
		reason = ReasonCounterReset
	} else {
		delta = usageUsec - job.CPUUsageUsec
		job.ServiceInLevelUsec += delta
	}
	job.CPUUsageUsec = usageUsec
	job.LastObservedAt = now

	if globalBoost && job.Level != model.QueueQ0 {
		e.moveTo(&job, model.QueueQ0, now)
		job.Promotions++
		reason = ReasonPriorityBoost
	} else if e.quantumExhausted(job) {
		e.moveTo(&job, job.Level.Demote(), now)
		job.Demotions++
		reason = ReasonQuantum
	} else if e.aged(job, now) {
		e.moveTo(&job, job.Level.Promote(), now)
		job.Promotions++
		reason = ReasonAging
	}
	job.CPUWeight = e.Weight(job.Level)

	return Evaluation{
		Job: job, PreviousLevel: previousLevel, Reason: reason, ServiceDeltaUsec: delta,
		LevelChanged: previousLevel != job.Level, WeightChanged: previousWeight != job.CPUWeight,
	}, nil
}

func (e *Engine) Weight(level model.QueueLevel) int {
	switch level {
	case model.QueueQ0:
		return e.config.Q0.Weight
	case model.QueueQ1:
		return e.config.Q1.Weight
	default:
		return e.config.Q2.Weight
	}
}

func (e *Engine) quantumExhausted(job model.ToolJob) bool {
	var quantum time.Duration
	switch job.Level {
	case model.QueueQ0:
		quantum = e.config.Q0.Quantum
	case model.QueueQ1:
		quantum = e.config.Q1.Quantum
	default:
		return false
	}
	return job.ServiceInLevelUsec >= uint64(quantum/time.Microsecond)
}

func (e *Engine) aged(job model.ToolJob, now time.Time) bool {
	switch job.Level {
	case model.QueueQ1:
		return now.Sub(job.LevelEnteredAt) >= e.config.Q1Aging
	case model.QueueQ2:
		return now.Sub(job.LevelEnteredAt) >= e.config.Q2Aging
	default:
		return false
	}
}

func (e *Engine) moveTo(job *model.ToolJob, level model.QueueLevel, now time.Time) {
	job.Level = level
	job.ServiceInLevelUsec = 0
	job.LevelEnteredAt = now
}

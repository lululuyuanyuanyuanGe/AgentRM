package model

import (
	"errors"
	"time"
)

type QueueLevel string

const (
	QueueQ0 QueueLevel = "Q0"
	QueueQ1 QueueLevel = "Q1"
	QueueQ2 QueueLevel = "Q2"
)

func (q QueueLevel) Valid() bool {
	return q == QueueQ0 || q == QueueQ1 || q == QueueQ2
}

func (q QueueLevel) Demote() QueueLevel {
	switch q {
	case QueueQ0:
		return QueueQ1
	case QueueQ1:
		return QueueQ2
	default:
		return QueueQ2
	}
}

func (q QueueLevel) Promote() QueueLevel {
	switch q {
	case QueueQ2:
		return QueueQ1
	case QueueQ1:
		return QueueQ0
	default:
		return QueueQ0
	}
}

type JobState string

const (
	JobRunning  JobState = "RUNNING"
	JobFinished JobState = "FINISHED"
	JobFailed   JobState = "FAILED"
)

func (s JobState) Valid() bool {
	return s == JobRunning || s == JobFinished || s == JobFailed
}

// ToolJob is the scheduler's complete view of one running tool invocation.
// It intentionally contains no semantic task class or predicted duration.
type ToolJob struct {
	ID                 string     `json:"job_id"`
	SandboxID          string     `json:"sandbox_id"`
	CgroupPath         string     `json:"cgroup_path"`
	State              JobState   `json:"state"`
	Level              QueueLevel `json:"queue"`
	CPUWeight          int        `json:"cpu_weight"`
	CPUUsageUsec       uint64     `json:"cpu_usage_usec"`
	ServiceInLevelUsec uint64     `json:"service_in_level_usec"`
	Demotions          int        `json:"demotions"`
	Promotions         int        `json:"promotions"`
	StartedAt          time.Time  `json:"started_at"`
	LevelEnteredAt     time.Time  `json:"level_entered_at"`
	LastObservedAt     time.Time  `json:"last_observed_at"`
	FinishedAt         *time.Time `json:"finished_at,omitempty"`
}

func (j ToolJob) Validate() error {
	if j.ID == "" {
		return errors.New("job_id is required")
	}
	if j.SandboxID == "" {
		return errors.New("sandbox_id is required")
	}
	if j.CgroupPath == "" {
		return errors.New("cgroup_path is required")
	}
	if !j.State.Valid() {
		return errors.New("job state is invalid")
	}
	if !j.Level.Valid() {
		return errors.New("queue level is invalid")
	}
	if j.CPUWeight < 1 || j.CPUWeight > 10000 {
		return errors.New("cpu_weight must be between 1 and 10000")
	}
	return nil
}

func (j ToolJob) Active() bool { return j.State == JobRunning }

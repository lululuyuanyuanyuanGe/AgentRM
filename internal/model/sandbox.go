package model

import (
	"errors"
	"fmt"
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

// SandboxEntity is one Agent Sandbox backing Pod tracked by the node scheduler.
// Queue credit belongs to the session and is never reset by individual tool calls.
type SandboxEntity struct {
	Namespace        string     `json:"namespace"`
	SandboxName      string     `json:"sandbox_name"`
	SandboxUID       string     `json:"sandbox_uid"`
	PodName          string     `json:"pod_name"`
	PodUID           string     `json:"pod_uid"`
	NodeName         string     `json:"node_name"`
	CgroupPath       string     `json:"cgroup_path"`
	CgroupID         uint64     `json:"cgroup_id"`
	Level            QueueLevel `json:"queue"`
	CPUWeight        int        `json:"cpu_weight"`
	BudgetNS         uint64     `json:"budget_ns,omitempty"`
	ServiceInLevelNS uint64     `json:"service_in_level_ns"`
	AccountedNS      uint64     `json:"accounted_ns"`
	Generation       uint32     `json:"generation"`
	Demotions        int        `json:"demotions"`
	Promotions       int        `json:"promotions"`
	StartedAt        time.Time  `json:"started_at"`
	LevelEnteredAt   time.Time  `json:"level_entered_at"`
	LastEventAt      time.Time  `json:"last_event_at"`
}

func (s SandboxEntity) Key() string { return s.PodUID }

func (s SandboxEntity) Validate() error {
	for field, value := range map[string]string{
		"namespace": s.Namespace, "sandbox_name": s.SandboxName, "sandbox_uid": s.SandboxUID,
		"pod_name": s.PodName, "pod_uid": s.PodUID, "node_name": s.NodeName, "cgroup_path": s.CgroupPath,
	} {
		if value == "" {
			return fmt.Errorf("%s is required", field)
		}
	}
	if s.CgroupID == 0 {
		return errors.New("cgroup_id is required")
	}
	if !s.Level.Valid() {
		return errors.New("queue level is invalid")
	}
	if s.CPUWeight < 1 || s.CPUWeight > 10000 {
		return errors.New("cpu_weight must be between 1 and 10000")
	}
	if s.Generation == 0 {
		return errors.New("generation is required")
	}
	return nil
}

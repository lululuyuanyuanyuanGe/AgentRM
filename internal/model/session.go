package model

import (
	"errors"
	"fmt"
	"time"
)

type SessionState string

const (
	SessionCreating        SessionState = "CREATING"
	SessionReady           SessionState = "READY"
	SessionRunningTool     SessionState = "RUNNING_TOOL"
	SessionWaitingLLM      SessionState = "WAITING_LLM"
	SessionWaitingUser     SessionState = "WAITING_USER"
	SessionBackground      SessionState = "BACKGROUND"
	SessionWaitingResource SessionState = "WAITING_RESOURCE"
	SessionSuspending      SessionState = "SUSPENDING"
	SessionSuspended       SessionState = "SUSPENDED"
	SessionResuming        SessionState = "RESUMING"
	SessionFinished        SessionState = "FINISHED"
	SessionFailed          SessionState = "FAILED"
)

func (s SessionState) Valid() bool {
	switch s {
	case SessionCreating, SessionReady, SessionRunningTool, SessionWaitingLLM,
		SessionWaitingUser, SessionBackground, SessionWaitingResource,
		SessionSuspending, SessionSuspended, SessionResuming, SessionFinished, SessionFailed:
		return true
	default:
		return false
	}
}

type TaskPriority int

const (
	PriorityBackground TaskPriority = iota
	PriorityNormal
	PriorityInteractive
)

type PodState string

const (
	PodUnknown PodState = "UNKNOWN"
	PodPending PodState = "PENDING"
	PodReady   PodState = "READY"
	PodFailed  PodState = "FAILED"
	PodDeleted PodState = "DELETED"
)

// Session is AgentRM's authoritative control-plane view of one sandbox.
type Session struct {
	ID                  string       `json:"session_id"`
	Min                 Resources    `json:"min_resource"`
	Max                 Resources    `json:"max_resource"`
	Desired             Resources    `json:"desired_resource"`
	Allocated           Resources    `json:"allocated_resource"`
	ActualCPU           int64        `json:"actual_cpu_milli"`
	MemoryWorkingSet    int64        `json:"memory_working_set_bytes"`
	MemoryStableSince   time.Time    `json:"memory_stable_since,omitempty"`
	State               SessionState `json:"session_state"`
	Priority            TaskPriority `json:"task_priority"`
	LastActiveAt        time.Time    `json:"last_active_at"`
	PodState            PodState     `json:"pod_state"`
	Generation          int64        `json:"generation"`
	AppliedGeneration   int64        `json:"applied_generation"`
	CheckpointReference string       `json:"checkpoint_reference,omitempty"`
	CreatedAt           time.Time    `json:"created_at"`
	UpdatedAt           time.Time    `json:"updated_at"`
}

func (s Session) Validate() error {
	if s.ID == "" {
		return errors.New("session_id is required")
	}
	if err := s.Min.Validate(); err != nil {
		return fmt.Errorf("min_resource: %w", err)
	}
	if err := s.Max.Validate(); err != nil {
		return fmt.Errorf("max_resource: %w", err)
	}
	if s.Min.CPUMilli > s.Max.CPUMilli || s.Min.MemoryBytes > s.Max.MemoryBytes {
		return errors.New("min_resource must not exceed max_resource")
	}
	if !s.State.Valid() {
		return errors.New("session_state is invalid")
	}
	if s.Priority < PriorityBackground || s.Priority > PriorityInteractive {
		return errors.New("task_priority is invalid")
	}
	return nil
}

func (s Session) Borrowed() Resources {
	return s.Allocated.SubFloor(s.Min)
}

func (s Session) IsTerminal() bool {
	return s.State == SessionFinished || s.State == SessionFailed
}

func (s Session) IsSuspendable() bool {
	switch s.State {
	case SessionReady, SessionWaitingLLM, SessionWaitingUser, SessionBackground, SessionWaitingResource:
		return true
	default:
		return false
	}
}

func (s Session) ReclaimClass(now time.Time, longIdleAfter time.Duration) int {
	if !s.LastActiveAt.IsZero() && now.Sub(s.LastActiveAt) >= longIdleAfter && s.IsSuspendable() {
		return 0
	}
	switch s.State {
	case SessionWaitingUser, SessionWaitingLLM, SessionWaitingResource, SessionReady:
		return 1
	case SessionBackground:
		return 2
	case SessionRunningTool:
		if s.Priority == PriorityInteractive {
			return 4
		}
		return 3
	default:
		return 3
	}
}

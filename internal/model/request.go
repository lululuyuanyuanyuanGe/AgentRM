package model

import (
	"errors"
	"time"
)

type ResourceRequest struct {
	SessionID  string       `json:"session_id"`
	Desired    Resources    `json:"desired_resource"`
	Generation int64        `json:"generation"`
	Priority   TaskPriority `json:"priority"`
	CreatedAt  time.Time    `json:"created_at"`
	NotBefore  time.Time    `json:"-"`
}

func (r ResourceRequest) Validate() error {
	if r.SessionID == "" {
		return errors.New("session_id is required")
	}
	if r.Generation <= 0 {
		return errors.New("generation must be positive")
	}
	if r.Priority < PriorityBackground || r.Priority > PriorityInteractive {
		return errors.New("priority is invalid")
	}
	return r.Desired.Validate()
}

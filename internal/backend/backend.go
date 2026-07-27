package backend

import (
	"context"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
)

type ResizeOperation struct {
	SessionID         string
	Target            model.Resources
	RequestGeneration int64
	Reason            string
}

// SandboxBackend is the narrow integration boundary between AgentRM and a
// sandbox implementation such as Kubernetes Agent Sandbox.
type SandboxBackend interface {
	Resize(context.Context, ResizeOperation) error
	Suspend(context.Context, string, int64) (checkpointReference string, err error)
	Resume(context.Context, string, string, model.Resources, int64) error
	Delete(context.Context, string) error
}

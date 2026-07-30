package backend

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
)

type memorySandbox struct {
	Resources  model.Resources
	Suspended  bool
	Checkpoint string
	Generation int64
}

// MemoryBackend makes the complete control plane runnable without a cluster.
// It is also useful as a deterministic integration-test backend.
type MemoryBackend struct {
	mu        sync.RWMutex
	sandboxes map[string]memorySandbox
}

func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{sandboxes: make(map[string]memorySandbox)}
}

func (b *MemoryBackend) Resize(_ context.Context, operation ResizeOperation) error {
	if operation.SessionID == "" {
		return errors.New("session id is required")
	}
	if err := operation.Target.Validate(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	sandbox := b.sandboxes[operation.SessionID]
	if sandbox.Suspended {
		return errors.New("cannot resize a suspended sandbox")
	}
	sandbox.Resources = operation.Target
	sandbox.Generation = operation.RequestGeneration
	b.sandboxes[operation.SessionID] = sandbox
	return nil
}

func (b *MemoryBackend) Suspend(_ context.Context, sessionID string, generation int64) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sandbox, ok := b.sandboxes[sessionID]
	if !ok {
		return "", errors.New("sandbox not found")
	}
	if sandbox.Suspended {
		return sandbox.Checkpoint, nil
	}
	reference := fmt.Sprintf("memory://checkpoint/%s/%d/%d", sessionID, generation, time.Now().UTC().UnixNano())
	sandbox.Suspended = true
	sandbox.Checkpoint = reference
	sandbox.Resources = model.Resources{}
	sandbox.Generation = generation
	b.sandboxes[sessionID] = sandbox
	return reference, nil
}

func (b *MemoryBackend) Resume(_ context.Context, sessionID, checkpointReference string, resources model.Resources, generation int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	sandbox, ok := b.sandboxes[sessionID]
	if !ok || !sandbox.Suspended {
		return errors.New("suspended sandbox not found")
	}
	if sandbox.Checkpoint != checkpointReference {
		return errors.New("checkpoint reference does not match")
	}
	sandbox.Suspended = false
	sandbox.Resources = resources
	sandbox.Generation = generation
	b.sandboxes[sessionID] = sandbox
	return nil
}

func (b *MemoryBackend) Delete(_ context.Context, sessionID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.sandboxes, sessionID)
	return nil
}

func (b *MemoryBackend) Resources(sessionID string) (model.Resources, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	sandbox, ok := b.sandboxes[sessionID]
	return sandbox.Resources, ok
}

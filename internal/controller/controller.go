package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/backend"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/queue"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/scheduler"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/store"
)

var (
	ErrStaleGeneration     = errors.New("stale request generation")
	ErrGenerationConflict  = errors.New("generation already has a different desired resource")
	ErrInsufficientMinimum = errors.New("cluster has insufficient capacity for session minimum")
)

type Controller struct {
	store      store.SessionStore
	queue      *queue.CoalescingQueue
	scheduler  *scheduler.Scheduler
	backend    backend.SandboxBackend
	now        func() time.Time
	resourceMu sync.Mutex
}

func New(sessionStore store.SessionStore, requestQueue *queue.CoalescingQueue, resourceScheduler *scheduler.Scheduler, sandboxBackend backend.SandboxBackend) *Controller {
	return &Controller{
		store: sessionStore, queue: requestQueue, scheduler: resourceScheduler, backend: sandboxBackend,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (c *Controller) CreateSession(ctx context.Context, session model.Session) (model.Session, error) {
	c.resourceMu.Lock()
	defer c.resourceMu.Unlock()
	if session.State == "" {
		session.State = model.SessionReady
	}
	if session.PodState == "" {
		session.PodState = model.PodReady
	}
	if err := session.Validate(); err != nil {
		return model.Session{}, err
	}
	if _, err := c.store.Get(session.ID); err == nil {
		return model.Session{}, store.ErrSessionExists
	} else if !errors.Is(err, store.ErrSessionNotFound) {
		return model.Session{}, err
	}

	used := totalAllocated(c.store.List())
	available := c.scheduler.Capacity().SubFloor(used)
	if session.Min.CPUMilli > available.CPUMilli || session.Min.MemoryBytes > available.MemoryBytes {
		return model.Session{}, ErrInsufficientMinimum
	}
	now := c.now()
	session.Desired = session.Min
	session.Allocated = session.Min
	session.Generation = 0
	session.AppliedGeneration = 0
	session.LastActiveAt = now
	session.CreatedAt = now
	session.UpdatedAt = now
	if err := c.backend.Resize(ctx, backend.ResizeOperation{SessionID: session.ID, Target: session.Min, Reason: "session_create"}); err != nil {
		return model.Session{}, err
	}
	if err := c.store.Create(session); err != nil {
		_ = c.backend.Delete(ctx, session.ID)
		return model.Session{}, err
	}
	return session, nil
}

func (c *Controller) GetSession(sessionID string) (model.Session, error) {
	return c.store.Get(sessionID)
}

func (c *Controller) ListSessions() []model.Session {
	return c.store.List()
}

func (c *Controller) RequestResources(request model.ResourceRequest) (model.Session, error) {
	c.resourceMu.Lock()
	defer c.resourceMu.Unlock()
	if err := request.Validate(); err != nil {
		return model.Session{}, err
	}
	session, err := c.store.Get(request.SessionID)
	if err != nil {
		return model.Session{}, err
	}
	if session.IsTerminal() || session.State == model.SessionSuspended || session.State == model.SessionSuspending {
		return model.Session{}, errors.New("session cannot request resources in its current state")
	}
	desired := request.Desired.Clamp(session.Min, session.Max)
	if request.Generation < session.Generation {
		return model.Session{}, ErrStaleGeneration
	}
	if request.Generation == session.Generation && desired != session.Desired {
		return model.Session{}, ErrGenerationConflict
	}

	updated, err := c.store.Update(request.SessionID, func(current *model.Session) error {
		current.Desired = desired
		current.Generation = request.Generation
		current.Priority = request.Priority
		current.LastActiveAt = c.now()
		return nil
	})
	if err != nil {
		return model.Session{}, err
	}
	request.Desired = desired
	c.queue.Enqueue(request)
	return updated, nil
}

// ProcessNext executes at most one coalesced request. Callers may run it from a
// fixed-rate reconcile loop; resource-starved requests are requeued.
func (c *Controller) ProcessNext(ctx context.Context) (scheduler.Plan, bool, error) {
	c.resourceMu.Lock()
	defer c.resourceMu.Unlock()
	request, ok := c.queue.Pop()
	if !ok {
		return scheduler.Plan{}, false, nil
	}
	session, err := c.store.Get(request.SessionID)
	if err != nil {
		return scheduler.Plan{}, true, err
	}
	if request.Generation < session.Generation {
		return scheduler.Plan{}, true, nil
	}
	plan, err := c.scheduler.Plan(c.store.List(), request)
	if err != nil {
		return scheduler.Plan{}, true, err
	}

	for _, adjustment := range plan.Victims {
		if err := c.applyResize(ctx, adjustment, request.Generation); err != nil {
			c.queue.Enqueue(request)
			return plan, true, fmt.Errorf("resize victim %s: %w", adjustment.SessionID, err)
		}
	}
	if err := c.applyResize(ctx, plan.Target, request.Generation); err != nil {
		c.queue.Enqueue(request)
		return plan, true, fmt.Errorf("resize target %s: %w", plan.Target.SessionID, err)
	}

	_, err = c.store.Update(request.SessionID, func(current *model.Session) error {
		if request.Generation < current.Generation {
			return nil
		}
		if !plan.Deferred {
			current.AppliedGeneration = request.Generation
		}
		if plan.Waiting {
			current.State = model.SessionWaitingResource
		} else if current.State == model.SessionWaitingResource {
			current.State = model.SessionReady
		}
		return nil
	})
	if err != nil {
		return plan, true, err
	}
	if plan.Deferred {
		request.NotBefore = c.now().Add(time.Second)
		c.queue.Enqueue(request)
	}
	for _, adjustment := range plan.Victims {
		victim, getErr := c.store.Get(adjustment.SessionID)
		if getErr == nil && victim.Generation > 0 && victim.Desired != victim.Allocated {
			c.queue.Enqueue(model.ResourceRequest{
				SessionID: victim.ID, Desired: victim.Desired, Generation: victim.Generation,
				Priority: victim.Priority, CreatedAt: c.now(), NotBefore: c.now().Add(time.Second),
			})
		}
	}
	return plan, true, nil
}

func (c *Controller) UpdateState(sessionID string, state model.SessionState, priority model.TaskPriority) (model.Session, error) {
	if !state.Valid() {
		return model.Session{}, errors.New("session_state is invalid")
	}
	if priority < model.PriorityBackground || priority > model.PriorityInteractive {
		return model.Session{}, errors.New("task_priority is invalid")
	}
	return c.store.Update(sessionID, func(session *model.Session) error {
		if session.IsTerminal() {
			return errors.New("terminal session state cannot be changed")
		}
		session.State = state
		session.Priority = priority
		session.LastActiveAt = c.now()
		return nil
	})
}

func (c *Controller) UpdateMetrics(sessionID string, actualCPUMilli, memoryWorkingSetBytes int64, stableSince time.Time) (model.Session, error) {
	if actualCPUMilli < 0 || memoryWorkingSetBytes < 0 {
		return model.Session{}, errors.New("metrics must be non-negative")
	}
	return c.store.Update(sessionID, func(session *model.Session) error {
		session.ActualCPU = actualCPUMilli
		session.MemoryWorkingSet = memoryWorkingSetBytes
		session.MemoryStableSince = stableSince
		return nil
	})
}

func (c *Controller) SuspendSession(ctx context.Context, sessionID string) (model.Session, error) {
	c.resourceMu.Lock()
	defer c.resourceMu.Unlock()
	session, err := c.store.Get(sessionID)
	if err != nil {
		return model.Session{}, err
	}
	if session.State == model.SessionSuspended {
		return session, nil
	}
	if !session.IsSuspendable() {
		return model.Session{}, errors.New("session is not suspendable in its current state")
	}
	_, err = c.store.Update(sessionID, func(current *model.Session) error {
		current.State = model.SessionSuspending
		return nil
	})
	if err != nil {
		return model.Session{}, err
	}
	checkpoint, suspendErr := c.backend.Suspend(ctx, sessionID, session.Generation)
	if suspendErr != nil {
		_, _ = c.store.Update(sessionID, func(current *model.Session) error {
			current.State = session.State
			return nil
		})
		return model.Session{}, suspendErr
	}
	return c.store.Update(sessionID, func(current *model.Session) error {
		current.State = model.SessionSuspended
		current.PodState = model.PodDeleted
		current.Allocated = model.Resources{}
		current.CheckpointReference = checkpoint
		return nil
	})
}

func (c *Controller) ResumeSession(ctx context.Context, sessionID string) (model.Session, error) {
	c.resourceMu.Lock()
	defer c.resourceMu.Unlock()
	session, err := c.store.Get(sessionID)
	if err != nil {
		return model.Session{}, err
	}
	if session.State != model.SessionSuspended {
		return model.Session{}, errors.New("session is not suspended")
	}
	available := c.scheduler.Capacity().SubFloor(totalAllocated(c.store.List()))
	if session.Min.CPUMilli > available.CPUMilli || session.Min.MemoryBytes > available.MemoryBytes {
		return model.Session{}, ErrInsufficientMinimum
	}
	_, err = c.store.Update(sessionID, func(current *model.Session) error {
		current.State = model.SessionResuming
		return nil
	})
	if err != nil {
		return model.Session{}, err
	}
	if err := c.backend.Resume(ctx, sessionID, session.CheckpointReference, session.Min, session.Generation); err != nil {
		_, _ = c.store.Update(sessionID, func(current *model.Session) error {
			current.State = model.SessionSuspended
			return nil
		})
		return model.Session{}, err
	}
	return c.store.Update(sessionID, func(current *model.Session) error {
		current.State = model.SessionReady
		current.PodState = model.PodReady
		current.Desired = current.Min
		current.Allocated = current.Min
		current.LastActiveAt = c.now()
		return nil
	})
}

func (c *Controller) FinishSession(ctx context.Context, sessionID string) (model.Session, error) {
	c.resourceMu.Lock()
	defer c.resourceMu.Unlock()
	if err := c.backend.Delete(ctx, sessionID); err != nil {
		return model.Session{}, err
	}
	return c.store.Update(sessionID, func(session *model.Session) error {
		session.State = model.SessionFinished
		session.PodState = model.PodDeleted
		session.Desired = model.Resources{}
		session.Allocated = model.Resources{}
		return nil
	})
}

func (c *Controller) PendingRequests() int { return c.queue.Len() }

type ClusterSnapshot struct {
	Capacity        model.Resources `json:"capacity"`
	Allocated       model.Resources `json:"allocated"`
	Free            model.Resources `json:"free"`
	PendingRequests int             `json:"pending_requests"`
}

func (c *Controller) ClusterSnapshot() ClusterSnapshot {
	capacity := c.scheduler.Capacity()
	allocated := totalAllocated(c.store.List())
	return ClusterSnapshot{
		Capacity: capacity, Allocated: allocated,
		Free: capacity.SubFloor(allocated), PendingRequests: c.queue.Len(),
	}
}

func (c *Controller) applyResize(ctx context.Context, adjustment scheduler.Adjustment, generation int64) error {
	if adjustment.Before == adjustment.After {
		return nil
	}
	if err := c.backend.Resize(ctx, backend.ResizeOperation{
		SessionID: adjustment.SessionID, Target: adjustment.After,
		RequestGeneration: generation, Reason: adjustment.Reason,
	}); err != nil {
		return err
	}
	_, err := c.store.Update(adjustment.SessionID, func(session *model.Session) error {
		session.Allocated = adjustment.After
		return nil
	})
	return err
}

func totalAllocated(sessions []model.Session) model.Resources {
	var result model.Resources
	for _, session := range sessions {
		result = result.Add(session.Allocated)
	}
	return result
}

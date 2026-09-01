package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/accounting"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/cgroup"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/discovery"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/mlfq"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/store"
)

type Snapshot struct {
	Sandboxes int `json:"sandboxes"`
	Q0        int `json:"q0"`
	Q1        int `json:"q1"`
	Q2        int `json:"q2"`
}

type Option func(*Controller)

func WithClock(clock func() time.Time) Option {
	return func(controller *Controller) { controller.now = clock }
}

func WithRetryInterval(interval time.Duration) Option {
	return func(controller *Controller) { controller.retryInterval = interval }
}

// Controller joins Kubernetes discovery, eBPF accounting and cgroup actuation.
// All mutations are serialized so a stale Ring Buffer event cannot race a Pod
// deletion or a global priority boost.
type Controller struct {
	store         store.SandboxStore
	cgroups       cgroup.Client
	resolver      cgroup.PodResolver
	accounting    accounting.Source
	policy        *mlfq.Policy
	logger        *slog.Logger
	now           func() time.Time
	retryInterval time.Duration
	mu            sync.Mutex
	pendingPods   map[string]discovery.SandboxPod
	pendingEvents map[string]accounting.Event
}

func NewController(
	entityStore store.SandboxStore,
	cgroups cgroup.Client,
	resolver cgroup.PodResolver,
	accountingSource accounting.Source,
	policy *mlfq.Policy,
	logger *slog.Logger,
	options ...Option,
) (*Controller, error) {
	if entityStore == nil || cgroups == nil || resolver == nil || accountingSource == nil || policy == nil {
		return nil, errors.New("store, cgroup client, resolver, accounting source and policy are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	controller := &Controller{
		store: entityStore, cgroups: cgroups, resolver: resolver, accounting: accountingSource,
		policy: policy, logger: logger, now: func() time.Time { return time.Now().UTC() },
		retryInterval: time.Second, pendingPods: make(map[string]discovery.SandboxPod),
		pendingEvents: make(map[string]accounting.Event),
	}
	for _, option := range options {
		option(controller)
	}
	if controller.retryInterval <= 0 {
		return nil, errors.New("retry interval must be positive")
	}
	return controller, nil
}

func (c *Controller) Run(ctx context.Context, podEvents <-chan discovery.SandboxPod) error {
	boostTicker := time.NewTicker(c.policy.Config().BoostInterval)
	retryTicker := time.NewTicker(c.retryInterval)
	defer boostTicker.Stop()
	defer retryTicker.Stop()

	thresholdEvents := c.accounting.Events()
	accountingErrors := c.accounting.Errors()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case pod, ok := <-podEvents:
			if !ok {
				podEvents = nil
				continue
			}
			if err := c.HandlePod(ctx, pod); err != nil {
				c.logger.Warn("sandbox Pod reconcile deferred", "pod_uid", pod.PodUID, "error", err)
			}
		case event, ok := <-thresholdEvents:
			if !ok {
				thresholdEvents = nil
				continue
			}
			if err := c.HandleThreshold(ctx, event); err != nil {
				c.logger.Error("CPU credit event failed", "cgroup_id", event.CgroupID, "error", err)
			}
		case err, ok := <-accountingErrors:
			if !ok {
				accountingErrors = nil
				continue
			}
			c.logger.Error("kernel accounting error", "error", err)
		case <-boostTicker.C:
			if err := c.Boost(ctx); err != nil {
				c.logger.Error("priority boost incomplete", "error", err)
			}
		case <-retryTicker.C:
			c.retry(ctx)
		}
	}
}

func (c *Controller) HandlePod(ctx context.Context, pod discovery.SandboxPod) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if pod.PodUID == "" {
		return errors.New("pod UID is required")
	}
	if pod.Type == discovery.EventDelete || !pod.Runnable() {
		delete(c.pendingPods, pod.PodUID)
		return c.removePodLocked(ctx, pod.PodUID)
	}
	if _, err := c.store.GetByPodUID(pod.PodUID); err == nil {
		delete(c.pendingPods, pod.PodUID)
		return nil
	} else if !errors.Is(err, store.ErrSandboxNotFound) {
		return err
	}
	location, err := c.resolver.ResolvePod(ctx, pod.PodUID)
	if err != nil {
		c.pendingPods[pod.PodUID] = pod
		return fmt.Errorf("resolve Pod cgroup: %w", err)
	}
	if existing, err := c.store.GetByCgroupID(location.ID); err == nil && existing.PodUID != pod.PodUID {
		return fmt.Errorf("cgroup ID %d is already owned by Pod %s", location.ID, existing.PodUID)
	}
	entity, err := c.policy.NewSandbox(model.SandboxEntity{
		Namespace: pod.Namespace, SandboxName: pod.SandboxName, SandboxUID: pod.SandboxUID,
		PodName: pod.PodName, PodUID: pod.PodUID, NodeName: pod.NodeName,
		CgroupPath: location.Path, CgroupID: location.ID,
	}, c.now())
	if err != nil {
		return err
	}
	if err := c.accounting.Configure(ctx, accountingConfig(entity)); err != nil {
		c.pendingPods[pod.PodUID] = pod
		return err
	}
	if err := c.cgroups.WriteWeight(ctx, entity.CgroupPath, entity.CPUWeight); err != nil {
		_ = c.accounting.Remove(ctx, entity.CgroupID)
		c.pendingPods[pod.PodUID] = pod
		return fmt.Errorf("set initial Q0 cpu.weight: %w", err)
	}
	c.store.Upsert(entity)
	delete(c.pendingPods, pod.PodUID)
	c.logger.Info("Agent Sandbox admitted", "namespace", entity.Namespace, "sandbox", entity.SandboxName, "pod", entity.PodName, "cgroup_id", entity.CgroupID)
	return nil
}

func (c *Controller) HandleThreshold(ctx context.Context, event accounting.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	entity, err := c.store.GetByCgroupID(event.CgroupID)
	if errors.Is(err, store.ErrSandboxNotFound) {
		return nil // stale event from a Pod that was already removed
	}
	if err != nil {
		return err
	}
	if event.Generation != entity.Generation || event.Level != accountingLevel(entity.Level) {
		return nil // delayed event from a previous queue generation
	}
	next, err := c.policy.Demote(entity, event.UsedNS, c.now())
	if err != nil {
		return err
	}
	if next.Level == entity.Level {
		return nil
	}
	key := eventKey(event)
	if err := c.applyTransitionLocked(ctx, entity, next); err != nil {
		c.pendingEvents[key] = event
		return err
	}
	delete(c.pendingEvents, key)
	c.logger.Info("Sandbox queue demoted", "namespace", next.Namespace, "sandbox", next.SandboxName, "from", entity.Level, "to", next.Level, "cpu_weight", next.CPUWeight)
	return nil
}

func (c *Controller) Boost(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result error
	for _, entity := range c.store.List() {
		if entity.Level == model.QueueQ0 {
			continue
		}
		next, err := c.policy.Boost(entity, c.now())
		if err == nil {
			err = c.applyTransitionLocked(ctx, entity, next)
		}
		if err != nil {
			result = errors.Join(result, fmt.Errorf("boost %s/%s: %w", entity.Namespace, entity.SandboxName, err))
			continue
		}
		c.logger.Info("Sandbox priority boosted", "namespace", next.Namespace, "sandbox", next.SandboxName, "from", entity.Level, "to", next.Level)
	}
	return result
}

func (c *Controller) Sandboxes() []model.SandboxEntity { return c.store.List() }

func (c *Controller) Config() mlfq.SessionConfig { return c.policy.Config() }

func (c *Controller) Snapshot() Snapshot {
	snapshot := Snapshot{}
	for _, entity := range c.store.List() {
		snapshot.Sandboxes++
		switch entity.Level {
		case model.QueueQ0:
			snapshot.Q0++
		case model.QueueQ1:
			snapshot.Q1++
		case model.QueueQ2:
			snapshot.Q2++
		}
	}
	return snapshot
}

func (c *Controller) removePodLocked(ctx context.Context, podUID string) error {
	entity, err := c.store.DeleteByPodUID(podUID)
	if errors.Is(err, store.ErrSandboxNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for key, event := range c.pendingEvents {
		if event.CgroupID == entity.CgroupID {
			delete(c.pendingEvents, key)
		}
	}
	removeErr := c.accounting.Remove(ctx, entity.CgroupID)
	weightErr := c.cgroups.WriteWeight(ctx, entity.CgroupPath, c.policy.Weight(model.QueueQ2))
	return errors.Join(removeErr, weightErr)
}

func (c *Controller) applyTransitionLocked(ctx context.Context, previous, next model.SandboxEntity) error {
	if err := c.cgroups.WriteWeight(ctx, next.CgroupPath, next.CPUWeight); err != nil {
		return fmt.Errorf("write cpu.weight: %w", err)
	}
	if err := c.accounting.Configure(ctx, accountingConfig(next)); err != nil {
		_ = c.cgroups.WriteWeight(ctx, previous.CgroupPath, previous.CPUWeight)
		return fmt.Errorf("configure next CPU credit: %w", err)
	}
	c.store.Upsert(next)
	return nil
}

func (c *Controller) retry(ctx context.Context) {
	c.mu.Lock()
	pods := make([]discovery.SandboxPod, 0, len(c.pendingPods))
	for _, pod := range c.pendingPods {
		pods = append(pods, pod)
	}
	events := make([]accounting.Event, 0, len(c.pendingEvents))
	for _, event := range c.pendingEvents {
		events = append(events, event)
	}
	c.mu.Unlock()
	for _, pod := range pods {
		if err := c.HandlePod(ctx, pod); err != nil {
			c.logger.Debug("Pod cgroup still unavailable", "pod_uid", pod.PodUID, "error", err)
		}
	}
	for _, event := range events {
		if err := c.HandleThreshold(ctx, event); err != nil {
			c.logger.Debug("CPU credit transition still pending", "cgroup_id", event.CgroupID, "error", err)
		}
	}
}

func accountingConfig(entity model.SandboxEntity) accounting.Configuration {
	return accounting.Configuration{
		CgroupID: entity.CgroupID, BudgetNS: entity.BudgetNS,
		Level: accountingLevel(entity.Level), Generation: entity.Generation,
	}
}

func accountingLevel(level model.QueueLevel) accounting.Level {
	switch level {
	case model.QueueQ0:
		return accounting.LevelQ0
	case model.QueueQ1:
		return accounting.LevelQ1
	default:
		return accounting.LevelQ2
	}
}

func eventKey(event accounting.Event) string {
	return fmt.Sprintf("%d/%d", event.CgroupID, event.Generation)
}

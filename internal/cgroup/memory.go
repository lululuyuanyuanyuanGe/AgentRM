package cgroup

import (
	"context"
	"errors"
	"sync"
)

// MemoryClient is a deterministic cgroup implementation for tests and demos.
type MemoryClient struct {
	mu      sync.RWMutex
	stats   map[string]CPUStat
	weights map[string]int
}

func NewMemoryClient() *MemoryClient {
	return &MemoryClient{stats: make(map[string]CPUStat), weights: make(map[string]int)}
}

func (c *MemoryClient) Add(cgroupPath string, stat CPUStat, weight int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats[cgroupPath] = stat
	c.weights[cgroupPath] = weight
}

func (c *MemoryClient) SetUsage(cgroupPath string, usageUsec uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	stat, ok := c.stats[cgroupPath]
	if !ok {
		return errors.New("cgroup not found")
	}
	stat.UsageUsec = usageUsec
	c.stats[cgroupPath] = stat
	return nil
}

func (c *MemoryClient) ReadCPUStat(ctx context.Context, cgroupPath string) (CPUStat, error) {
	if err := ctx.Err(); err != nil {
		return CPUStat{}, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	stat, ok := c.stats[cgroupPath]
	if !ok {
		return CPUStat{}, errors.New("cgroup not found")
	}
	return stat, nil
}

func (c *MemoryClient) ReadWeight(ctx context.Context, cgroupPath string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	weight, ok := c.weights[cgroupPath]
	if !ok {
		return 0, errors.New("cgroup not found")
	}
	return weight, nil
}

func (c *MemoryClient) WriteWeight(ctx context.Context, cgroupPath string, weight int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if weight < 1 || weight > 10000 {
		return errors.New("cpu.weight must be between 1 and 10000")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.stats[cgroupPath]; !ok {
		return errors.New("cgroup not found")
	}
	c.weights[cgroupPath] = weight
	return nil
}

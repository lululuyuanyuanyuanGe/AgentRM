package accounting

import (
	"context"
	"errors"
	"sync"
)

type Level uint32

const (
	LevelQ0 Level = iota
	LevelQ1
	LevelQ2
)

type Configuration struct {
	CgroupID   uint64
	BudgetNS   uint64
	Level      Level
	Generation uint32
}

func (c Configuration) Validate() error {
	if c.CgroupID == 0 {
		return errors.New("cgroup ID is required")
	}
	if c.Level > LevelQ2 {
		return errors.New("accounting level is invalid")
	}
	if c.Generation == 0 {
		return errors.New("generation is required")
	}
	if c.Level != LevelQ2 && c.BudgetNS == 0 {
		return errors.New("Q0 and Q1 require a CPU service budget")
	}
	if c.Level == LevelQ2 && c.BudgetNS != 0 {
		return errors.New("Q2 must not emit a budget event")
	}
	return nil
}

type Event struct {
	CgroupID    uint64
	UsedNS      uint64
	BudgetNS    uint64
	TimestampNS uint64
	Level       Level
	Generation  uint32
}

type Source interface {
	Configure(context.Context, Configuration) error
	Remove(context.Context, uint64) error
	Events() <-chan Event
	Errors() <-chan error
	Close() error
}

// MemorySource keeps the same generation and threshold semantics as the eBPF
// source so policy/controller tests do not require a Linux kernel.
type MemorySource struct {
	mu      sync.RWMutex
	configs map[uint64]Configuration
	events  chan Event
	errors  chan error
}

func NewMemorySource() *MemorySource {
	return &MemorySource{
		configs: make(map[uint64]Configuration),
		events:  make(chan Event, 64), errors: make(chan error, 1),
	}
}

func (s *MemorySource) Configure(_ context.Context, config Configuration) error {
	if err := config.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.configs[config.CgroupID] = config
	s.mu.Unlock()
	return nil
}

func (s *MemorySource) Remove(_ context.Context, cgroupID uint64) error {
	s.mu.Lock()
	delete(s.configs, cgroupID)
	s.mu.Unlock()
	return nil
}

func (s *MemorySource) Events() <-chan Event { return s.events }
func (s *MemorySource) Errors() <-chan error { return s.errors }
func (s *MemorySource) Close() error         { return nil }

func (s *MemorySource) Configuration(cgroupID uint64) (Configuration, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	config, ok := s.configs[cgroupID]
	return config, ok
}

func (s *MemorySource) Exhaust(cgroupID uint64) error {
	s.mu.RLock()
	config, ok := s.configs[cgroupID]
	s.mu.RUnlock()
	if !ok {
		return errors.New("cgroup is not configured")
	}
	if config.BudgetNS == 0 {
		return errors.New("cgroup has no finite budget")
	}
	s.events <- Event{
		CgroupID: cgroupID, UsedNS: config.BudgetNS, BudgetNS: config.BudgetNS,
		Level: config.Level, Generation: config.Generation,
	}
	return nil
}

package mlfq

import (
	"errors"
	"fmt"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
)

type SessionLevel struct {
	Weight int
	Budget time.Duration
}

type SessionConfig struct {
	Q0            SessionLevel
	Q1            SessionLevel
	Q2            SessionLevel
	BoostInterval time.Duration
}

func DefaultSessionConfig() SessionConfig {
	return SessionConfig{
		Q0:            SessionLevel{Weight: 1000, Budget: 4 * time.Second},
		Q1:            SessionLevel{Weight: 300, Budget: 20 * time.Second},
		Q2:            SessionLevel{Weight: 100},
		BoostInterval: 60 * time.Second,
	}
}

func (c SessionConfig) Validate() error {
	for level, weight := range map[string]int{"Q0": c.Q0.Weight, "Q1": c.Q1.Weight, "Q2": c.Q2.Weight} {
		if weight < 1 || weight > 10000 {
			return fmt.Errorf("%s weight must be between 1 and 10000", level)
		}
	}
	if !(c.Q0.Weight > c.Q1.Weight && c.Q1.Weight > c.Q2.Weight) {
		return errors.New("queue weights must satisfy Q0 > Q1 > Q2")
	}
	if c.Q0.Budget <= 0 || c.Q1.Budget <= 0 {
		return errors.New("Q0 and Q1 CPU service budgets must be positive")
	}
	if c.Q2.Budget != 0 {
		return errors.New("Q2 must not have a finite CPU service budget")
	}
	if c.BoostInterval <= 0 {
		return errors.New("boost interval must be positive")
	}
	return nil
}

type Policy struct{ config SessionConfig }

func NewPolicy(config SessionConfig) (*Policy, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Policy{config: config}, nil
}

func (p *Policy) Config() SessionConfig { return p.config }

func (p *Policy) NewSandbox(entity model.SandboxEntity, now time.Time) (model.SandboxEntity, error) {
	entity.Level = model.QueueQ0
	entity.CPUWeight = p.Weight(model.QueueQ0)
	entity.BudgetNS = p.BudgetNS(model.QueueQ0)
	entity.Generation = 1
	entity.StartedAt = now
	entity.LevelEnteredAt = now
	entity.LastEventAt = now
	return entity, entity.Validate()
}

func (p *Policy) Demote(entity model.SandboxEntity, usedNS uint64, now time.Time) (model.SandboxEntity, error) {
	if err := entity.Validate(); err != nil {
		return model.SandboxEntity{}, err
	}
	if entity.Level == model.QueueQ2 {
		return entity, nil
	}
	if usedNS < entity.BudgetNS {
		return model.SandboxEntity{}, fmt.Errorf("CPU service %d did not exhaust budget %d", usedNS, entity.BudgetNS)
	}
	entity.AccountedNS += usedNS
	entity.ServiceInLevelNS = 0
	entity.Level = entity.Level.Demote()
	entity.CPUWeight = p.Weight(entity.Level)
	entity.BudgetNS = p.BudgetNS(entity.Level)
	entity.Generation++
	entity.Demotions++
	entity.LevelEnteredAt = now
	entity.LastEventAt = now
	return entity, nil
}

func (p *Policy) Boost(entity model.SandboxEntity, now time.Time) (model.SandboxEntity, error) {
	if err := entity.Validate(); err != nil {
		return model.SandboxEntity{}, err
	}
	if entity.Level == model.QueueQ0 {
		return entity, nil
	}
	entity.Level = model.QueueQ0
	entity.CPUWeight = p.Weight(model.QueueQ0)
	entity.BudgetNS = p.BudgetNS(model.QueueQ0)
	entity.ServiceInLevelNS = 0
	entity.Generation++
	entity.Promotions++
	entity.LevelEnteredAt = now
	entity.LastEventAt = now
	return entity, nil
}

func (p *Policy) Weight(level model.QueueLevel) int {
	switch level {
	case model.QueueQ0:
		return p.config.Q0.Weight
	case model.QueueQ1:
		return p.config.Q1.Weight
	default:
		return p.config.Q2.Weight
	}
}

func (p *Policy) BudgetNS(level model.QueueLevel) uint64 {
	switch level {
	case model.QueueQ0:
		return uint64(p.config.Q0.Budget)
	case model.QueueQ1:
		return uint64(p.config.Q1.Budget)
	default:
		return 0
	}
}

package scheduler

import (
	"errors"
	"sort"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
)

type Config struct {
	Capacity              model.Resources
	LongIdleAfter         time.Duration
	MemoryStableFor       time.Duration
	MemoryHeadroomPercent int64
}

func DefaultConfig(capacity model.Resources) Config {
	return Config{
		Capacity:              capacity,
		LongIdleAfter:         10 * time.Minute,
		MemoryStableFor:       2 * time.Minute,
		MemoryHeadroomPercent: 125,
	}
}

type Adjustment struct {
	SessionID string          `json:"session_id"`
	Before    model.Resources `json:"before"`
	After     model.Resources `json:"after"`
	Reason    string          `json:"reason"`
}

type Plan struct {
	RequestGeneration int64           `json:"request_generation"`
	Target            Adjustment      `json:"target"`
	Victims           []Adjustment    `json:"victims"`
	Shortfall         model.Resources `json:"shortfall"`
	Waiting           bool            `json:"waiting"`
	Deferred          bool            `json:"deferred"`
}

type Scheduler struct {
	config Config
	now    func() time.Time
}

func New(config Config) (*Scheduler, error) {
	if err := config.Capacity.Validate(); err != nil {
		return nil, err
	}
	if config.Capacity.IsZero() {
		return nil, errors.New("cluster capacity must be non-zero")
	}
	if config.LongIdleAfter <= 0 || config.MemoryStableFor <= 0 {
		return nil, errors.New("scheduler durations must be positive")
	}
	if config.MemoryHeadroomPercent < 100 {
		return nil, errors.New("memory headroom percent must be at least 100")
	}
	return &Scheduler{config: config, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Scheduler) Capacity() model.Resources { return s.config.Capacity }

func (s *Scheduler) Plan(sessions []model.Session, request model.ResourceRequest) (Plan, error) {
	var target *model.Session
	for index := range sessions {
		if sessions[index].ID == request.SessionID {
			target = &sessions[index]
			break
		}
	}
	if target == nil {
		return Plan{}, errors.New("target session not found")
	}
	if target.IsTerminal() || target.State == model.SessionSuspended || target.State == model.SessionSuspending {
		return Plan{}, errors.New("target session cannot be resized in its current state")
	}

	desired := request.Desired.Clamp(target.Min, target.Max)
	after := target.Allocated
	now := s.now()

	// CPU shrink is safe and immediate. Memory shrink only occurs after a
	// stable working-set observation with configured headroom.
	if desired.CPUMilli < after.CPUMilli {
		after.CPUMilli = desired.CPUMilli
	}
	if desired.MemoryBytes < after.MemoryBytes && s.canShrinkMemory(*target, desired.MemoryBytes, now) {
		after.MemoryBytes = desired.MemoryBytes
	}

	used := allocated(sessions)
	free := s.config.Capacity.SubFloor(used)
	need := desired.SubFloor(after)
	direct := need.Min(free)
	after = after.Add(direct)
	need = desired.SubFloor(after)

	victims := s.sortedVictims(sessions, *target, now)
	adjustments := make([]Adjustment, 0)
	for _, victim := range victims {
		if need.IsZero() {
			break
		}
		victimAfter := victim.Allocated
		cpuReclaimable := max(0, victimAfter.CPUMilli-victim.Min.CPUMilli)
		cpuReclaim := min(cpuReclaimable, need.CPUMilli)
		victimAfter.CPUMilli -= cpuReclaim
		after.CPUMilli += cpuReclaim
		need.CPUMilli -= cpuReclaim

		memoryFloor := s.memoryFloor(victim, now)
		memoryReclaimable := max(0, victimAfter.MemoryBytes-memoryFloor)
		memoryReclaim := min(memoryReclaimable, need.MemoryBytes)
		victimAfter.MemoryBytes -= memoryReclaim
		after.MemoryBytes += memoryReclaim
		need.MemoryBytes -= memoryReclaim

		if victimAfter != victim.Allocated {
			adjustments = append(adjustments, Adjustment{
				SessionID: victim.ID,
				Before:    victim.Allocated,
				After:     victimAfter,
				Reason:    "priority_reclamation",
			})
		}
	}

	shortfall := desired.SubFloor(after)
	return Plan{
		RequestGeneration: request.Generation,
		Target: Adjustment{
			SessionID: target.ID,
			Before:    target.Allocated,
			After:     after,
			Reason:    "resource_request",
		},
		Victims:   adjustments,
		Shortfall: shortfall,
		Waiting:   !shortfall.IsZero(),
		Deferred:  after != desired,
	}, nil
}

func allocated(sessions []model.Session) model.Resources {
	var result model.Resources
	for _, session := range sessions {
		result = result.Add(session.Allocated)
	}
	return result
}

func (s *Scheduler) sortedVictims(sessions []model.Session, target model.Session, now time.Time) []model.Session {
	result := make([]model.Session, 0, len(sessions))
	targetClass := target.ReclaimClass(now, s.config.LongIdleAfter)
	for _, session := range sessions {
		if session.ID == target.ID || session.IsTerminal() || session.State == model.SessionSuspended {
			continue
		}
		victimClass := session.ReclaimClass(now, s.config.LongIdleAfter)
		if victimClass >= targetClass {
			continue
		}
		if session.Borrowed().IsZero() {
			continue
		}
		result = append(result, session)
	}
	sort.Slice(result, func(i, j int) bool {
		leftClass := result[i].ReclaimClass(now, s.config.LongIdleAfter)
		rightClass := result[j].ReclaimClass(now, s.config.LongIdleAfter)
		if leftClass != rightClass {
			return leftClass < rightClass
		}
		leftBorrowed := result[i].Borrowed().CPUMilli
		rightBorrowed := result[j].Borrowed().CPUMilli
		if leftBorrowed != rightBorrowed {
			return leftBorrowed > rightBorrowed
		}
		if !result[i].LastActiveAt.Equal(result[j].LastActiveAt) {
			return result[i].LastActiveAt.Before(result[j].LastActiveAt)
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func (s *Scheduler) canShrinkMemory(session model.Session, target int64, now time.Time) bool {
	if session.MemoryStableSince.IsZero() || now.Sub(session.MemoryStableSince) < s.config.MemoryStableFor {
		return false
	}
	floor := s.memoryFloor(session, now)
	return target >= floor
}

func (s *Scheduler) memoryFloor(session model.Session, now time.Time) int64 {
	if session.MemoryStableSince.IsZero() || now.Sub(session.MemoryStableSince) < s.config.MemoryStableFor {
		return session.Allocated.MemoryBytes
	}
	headroom := ceilPercent(session.MemoryWorkingSet, s.config.MemoryHeadroomPercent)
	return max(session.Min.MemoryBytes, headroom)
}

func ceilPercent(value, percent int64) int64 {
	return (value*percent + 99) / 100
}

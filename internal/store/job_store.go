package store

import (
	"errors"
	"sort"
	"sync"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
)

var (
	ErrJobNotFound = errors.New("job not found")
	ErrJobExists   = errors.New("job already exists")
)

type JobStore interface {
	Create(model.ToolJob) error
	Get(jobID string) (model.ToolJob, error)
	List() []model.ToolJob
	Update(jobID string, update func(*model.ToolJob) error) (model.ToolJob, error)
}

type MemoryJobStore struct {
	mu   sync.RWMutex
	jobs map[string]model.ToolJob
}

func NewMemoryJobStore() *MemoryJobStore {
	return &MemoryJobStore{jobs: make(map[string]model.ToolJob)}
}

func (s *MemoryJobStore) Create(job model.ToolJob) error {
	if err := job.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; exists {
		return ErrJobExists
	}
	s.jobs[job.ID] = job
	return nil
}

func (s *MemoryJobStore) Get(jobID string) (model.ToolJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return model.ToolJob{}, ErrJobNotFound
	}
	return job, nil
}

func (s *MemoryJobStore) List() []model.ToolJob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]model.ToolJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		result = append(result, job)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (s *MemoryJobStore) Update(jobID string, update func(*model.ToolJob) error) (model.ToolJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return model.ToolJob{}, ErrJobNotFound
	}
	if err := update(&job); err != nil {
		return model.ToolJob{}, err
	}
	if err := job.Validate(); err != nil {
		return model.ToolJob{}, err
	}
	s.jobs[jobID] = job
	return job, nil
}

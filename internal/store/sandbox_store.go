package store

import (
	"errors"
	"sort"
	"sync"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
)

var ErrSandboxNotFound = errors.New("sandbox scheduling entity not found")

type SandboxStore interface {
	Upsert(model.SandboxEntity)
	GetByPodUID(string) (model.SandboxEntity, error)
	GetByCgroupID(uint64) (model.SandboxEntity, error)
	DeleteByPodUID(string) (model.SandboxEntity, error)
	List() []model.SandboxEntity
}

type MemorySandboxStore struct {
	mu       sync.RWMutex
	byPodUID map[string]model.SandboxEntity
	byCgroup map[uint64]string
}

func NewMemorySandboxStore() *MemorySandboxStore {
	return &MemorySandboxStore{byPodUID: make(map[string]model.SandboxEntity), byCgroup: make(map[uint64]string)}
}

func (s *MemorySandboxStore) Upsert(entity model.SandboxEntity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.byPodUID[entity.PodUID]; ok && previous.CgroupID != entity.CgroupID {
		delete(s.byCgroup, previous.CgroupID)
	}
	s.byPodUID[entity.PodUID] = entity
	s.byCgroup[entity.CgroupID] = entity.PodUID
}

func (s *MemorySandboxStore) GetByPodUID(uid string) (model.SandboxEntity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entity, ok := s.byPodUID[uid]
	if !ok {
		return model.SandboxEntity{}, ErrSandboxNotFound
	}
	return entity, nil
}

func (s *MemorySandboxStore) GetByCgroupID(id uint64) (model.SandboxEntity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	uid, ok := s.byCgroup[id]
	if !ok {
		return model.SandboxEntity{}, ErrSandboxNotFound
	}
	return s.byPodUID[uid], nil
}

func (s *MemorySandboxStore) DeleteByPodUID(uid string) (model.SandboxEntity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entity, ok := s.byPodUID[uid]
	if !ok {
		return model.SandboxEntity{}, ErrSandboxNotFound
	}
	delete(s.byPodUID, uid)
	delete(s.byCgroup, entity.CgroupID)
	return entity, nil
}

func (s *MemorySandboxStore) List() []model.SandboxEntity {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]model.SandboxEntity, 0, len(s.byPodUID))
	for _, entity := range s.byPodUID {
		items = append(items, entity)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Namespace == items[j].Namespace {
			return items[i].SandboxName < items[j].SandboxName
		}
		return items[i].Namespace < items[j].Namespace
	})
	return items
}

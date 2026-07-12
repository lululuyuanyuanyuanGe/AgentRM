package store

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
)

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrSessionExists   = errors.New("session already exists")
)

type SessionStore interface {
	Create(session model.Session) error
	Get(sessionID string) (model.Session, error)
	List() []model.Session
	Update(sessionID string, update func(*model.Session) error) (model.Session, error)
}

type MemorySessionStore struct {
	mu       sync.RWMutex
	sessions map[string]model.Session
}

func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{sessions: make(map[string]model.Session)}
}

func (s *MemorySessionStore) Create(session model.Session) error {
	if err := session.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessions[session.ID]; exists {
		return ErrSessionExists
	}
	now := time.Now().UTC()
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = now
	}
	if session.LastActiveAt.IsZero() {
		session.LastActiveAt = now
	}
	s.sessions[session.ID] = session
	return nil
}

func (s *MemorySessionStore) Get(sessionID string) (model.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return model.Session{}, ErrSessionNotFound
	}
	return session, nil
}

func (s *MemorySessionStore) List() []model.Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]model.Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		result = append(result, session)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (s *MemorySessionStore) Update(sessionID string, update func(*model.Session) error) (model.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return model.Session{}, ErrSessionNotFound
	}
	if err := update(&session); err != nil {
		return model.Session{}, err
	}
	session.UpdatedAt = time.Now().UTC()
	s.sessions[sessionID] = session
	return session, nil
}

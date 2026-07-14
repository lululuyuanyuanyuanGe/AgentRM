package queue

import (
	"container/heap"
	"sync"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
)

type item struct {
	request  model.ResourceRequest
	sequence uint64
	index    int
}

type priorityQueue []*item

func (q priorityQueue) Len() int { return len(q) }

func (q priorityQueue) Less(i, j int) bool {
	if q[i].request.Priority != q[j].request.Priority {
		return q[i].request.Priority > q[j].request.Priority
	}
	if !q[i].request.CreatedAt.Equal(q[j].request.CreatedAt) {
		return q[i].request.CreatedAt.Before(q[j].request.CreatedAt)
	}
	return q[i].sequence < q[j].sequence
}

func (q priorityQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index = i
	q[j].index = j
}

func (q *priorityQueue) Push(value any) {
	next := value.(*item)
	next.index = len(*q)
	*q = append(*q, next)
}

func (q *priorityQueue) Pop() any {
	old := *q
	n := len(old)
	next := old[n-1]
	next.index = -1
	*q = old[:n-1]
	return next
}

// CoalescingQueue keeps exactly one authoritative pending request per session.
// Superseded heap entries are discarded lazily when popped.
type CoalescingQueue struct {
	mu       sync.Mutex
	items    priorityQueue
	pending  map[string]*item
	sequence uint64
}

func New() *CoalescingQueue {
	q := &CoalescingQueue{pending: make(map[string]*item)}
	heap.Init(&q.items)
	return q
}

// Enqueue returns false for stale generations. Equal generations replace the
// request, making retries idempotent while allowing priority refreshes.
func (q *CoalescingQueue) Enqueue(request model.ResourceRequest) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	if request.CreatedAt.IsZero() {
		request.CreatedAt = time.Now().UTC()
	}
	if current, ok := q.pending[request.SessionID]; ok && request.Generation < current.request.Generation {
		return false
	}

	q.sequence++
	next := &item{request: request, sequence: q.sequence}
	q.pending[request.SessionID] = next
	heap.Push(&q.items, next)
	return true
}

func (q *CoalescingQueue) Pop() (model.ResourceRequest, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now().UTC()
	deferred := make([]*item, 0)
	for q.items.Len() > 0 {
		next := heap.Pop(&q.items).(*item)
		current, ok := q.pending[next.request.SessionID]
		if !ok || current.sequence != next.sequence {
			continue
		}
		if !next.request.NotBefore.IsZero() && next.request.NotBefore.After(now) {
			deferred = append(deferred, next)
			continue
		}
		for _, item := range deferred {
			heap.Push(&q.items, item)
		}
		delete(q.pending, next.request.SessionID)
		return next.request, true
	}
	for _, item := range deferred {
		heap.Push(&q.items, item)
	}
	return model.ResourceRequest{}, false
}

func (q *CoalescingQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

func (q *CoalescingQueue) Pending(sessionID string) (model.ResourceRequest, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	next, ok := q.pending[sessionID]
	if !ok {
		return model.ResourceRequest{}, false
	}
	return next.request, true
}

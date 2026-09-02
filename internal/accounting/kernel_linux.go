//go:build linux

package accounting

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"
)

type kernelObjects struct {
	Entities       *ebpf.Map     `ebpf:"entities"`
	Memberships    *ebpf.Map     `ebpf:"memberships"`
	Events         *ebpf.Map     `ebpf:"events"`
	AccountRuntime *ebpf.Program `ebpf:"account_runtime"`
}

func (o *kernelObjects) Close() {
	if o.AccountRuntime != nil {
		o.AccountRuntime.Close()
	}
	if o.Entities != nil {
		o.Entities.Close()
	}
	if o.Memberships != nil {
		o.Memberships.Close()
	}
	if o.Events != nil {
		o.Events.Close()
	}
}

type KernelSource struct {
	objects   kernelObjects
	hook      link.Link
	reader    *ringbuf.Reader
	events    chan Event
	errors    chan error
	close     sync.Once
	wg        sync.WaitGroup
	membersMu sync.Mutex
	members   map[uint64][]uint64
}

func NewKernelSource(objectPath string) (*KernelSource, error) {
	if objectPath == "" {
		return nil, errors.New("eBPF object path is required")
	}
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("remove memlock limit: %w", err)
	}
	spec, err := ebpf.LoadCollectionSpec(objectPath)
	if err != nil {
		return nil, fmt.Errorf("load eBPF collection spec: %w", err)
	}
	source := &KernelSource{events: make(chan Event, 256), errors: make(chan error, 8), members: make(map[uint64][]uint64)}
	if err := spec.LoadAndAssign(&source.objects, nil); err != nil {
		return nil, fmt.Errorf("load eBPF objects: %w", err)
	}
	source.hook, err = link.Tracepoint("sched", "sched_switch", source.objects.AccountRuntime, nil)
	if err != nil {
		source.objects.Close()
		return nil, fmt.Errorf("attach sched_switch tracepoint: %w", err)
	}
	source.reader, err = ringbuf.NewReader(source.objects.Events)
	if err != nil {
		source.hook.Close()
		source.objects.Close()
		return nil, fmt.Errorf("open threshold ring buffer: %w", err)
	}
	source.wg.Add(1)
	go source.readEvents()
	return source, nil
}

func (s *KernelSource) Configure(_ context.Context, config Configuration) error {
	if err := config.Validate(); err != nil {
		return err
	}
	state := bpfEntityState{BudgetNS: config.BudgetNS, Level: uint32(config.Level), Generation: config.Generation}
	var previous bpfEntityState
	hadPrevious := s.objects.Entities.Lookup(config.CgroupID, &previous) == nil
	if err := s.objects.Entities.Put(config.CgroupID, state); err != nil {
		return fmt.Errorf("configure cgroup accounting: %w", err)
	}
	if err := s.SyncMembers(context.Background(), config.CgroupID, config.MemberIDs); err != nil {
		if hadPrevious {
			_ = s.objects.Entities.Put(config.CgroupID, previous)
		} else {
			_ = s.objects.Entities.Delete(config.CgroupID)
		}
		return err
	}
	return nil
}

func (s *KernelSource) SyncMembers(_ context.Context, cgroupID uint64, memberIDs []uint64) error {
	if cgroupID == 0 || len(memberIDs) == 0 {
		return errors.New("cgroup ID and member cgroup IDs are required")
	}
	s.membersMu.Lock()
	defer s.membersMu.Unlock()
	next := make(map[uint64]struct{}, len(memberIDs))
	for _, memberID := range memberIDs {
		if memberID == 0 {
			return errors.New("member cgroup ID must be non-zero")
		}
		var owner uint64
		if err := s.objects.Memberships.Lookup(memberID, &owner); err == nil && owner != cgroupID {
			return fmt.Errorf("member cgroup ID %d belongs to Sandbox cgroup %d", memberID, owner)
		} else if err != nil && !errors.Is(err, ebpf.ErrKeyNotExist) {
			return fmt.Errorf("lookup cgroup membership: %w", err)
		}
		if err := s.objects.Memberships.Put(memberID, cgroupID); err != nil {
			return fmt.Errorf("store cgroup membership: %w", err)
		}
		next[memberID] = struct{}{}
	}
	for _, previousID := range s.members[cgroupID] {
		if _, keep := next[previousID]; keep {
			continue
		}
		var owner uint64
		if err := s.objects.Memberships.Lookup(previousID, &owner); err == nil && owner == cgroupID {
			_ = s.objects.Memberships.Delete(previousID)
		}
	}
	s.members[cgroupID] = append([]uint64(nil), memberIDs...)
	return nil
}

func (s *KernelSource) Remove(_ context.Context, cgroupID uint64) error {
	s.membersMu.Lock()
	for _, memberID := range s.members[cgroupID] {
		var owner uint64
		if err := s.objects.Memberships.Lookup(memberID, &owner); err == nil && owner == cgroupID {
			_ = s.objects.Memberships.Delete(memberID)
		}
	}
	delete(s.members, cgroupID)
	s.membersMu.Unlock()
	if err := s.objects.Entities.Delete(cgroupID); err != nil && !errors.Is(err, ebpf.ErrKeyNotExist) {
		return fmt.Errorf("remove cgroup accounting: %w", err)
	}
	return nil
}

func (s *KernelSource) Events() <-chan Event { return s.events }
func (s *KernelSource) Errors() <-chan error { return s.errors }

func (s *KernelSource) Close() error {
	var closeErr error
	s.close.Do(func() {
		if err := s.reader.Close(); err != nil {
			closeErr = err
		}
		s.wg.Wait()
		close(s.events)
		close(s.errors)
		closeErr = errors.Join(closeErr, s.hook.Close())
		s.objects.Close()
	})
	return closeErr
}

func (s *KernelSource) readEvents() {
	defer s.wg.Done()
	for {
		record, err := s.reader.Read()
		if err != nil {
			if errors.Is(err, ringbuf.ErrClosed) {
				return
			}
			s.reportError(fmt.Errorf("read threshold ring buffer: %w", err))
			continue
		}
		var raw bpfThresholdEvent
		if err := binary.Read(bytes.NewReader(record.RawSample), binary.LittleEndian, &raw); err != nil {
			if !errors.Is(err, io.EOF) {
				s.reportError(fmt.Errorf("decode threshold event: %w", err))
			}
			continue
		}
		s.events <- Event{
			CgroupID: raw.CgroupID, UsedNS: raw.UsedNS, BudgetNS: raw.BudgetNS,
			TimestampNS: raw.TimestampNS, Level: Level(raw.Level), Generation: raw.Generation,
		}
	}
}

func (s *KernelSource) reportError(err error) {
	select {
	case s.errors <- err:
	default:
	}
}

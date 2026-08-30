package cgroup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

var (
	ErrPodCgroupNotFound = errors.New("pod cgroup not found")
	ErrAmbiguousCgroup   = errors.New("multiple pod cgroups matched")
)

type Location struct {
	Path string
	ID   uint64
}

type PodResolver interface {
	ResolvePod(context.Context, string) (Location, error)
}

// FSResolver discovers the Pod-level cgroup created by kubelet. Both the
// systemd and cgroupfs naming conventions are supported; container children
// are ignored because their basename does not encode the Pod UID.
type FSResolver struct{ root string }

func NewFSResolver(root string) (*FSResolver, error) {
	client, err := NewFSClient(root)
	if err != nil {
		return nil, err
	}
	return &FSResolver{root: client.root}, nil
}

func (r *FSResolver) ResolvePod(ctx context.Context, podUID string) (Location, error) {
	uid := strings.ToLower(strings.TrimSpace(podUID))
	if uid == "" || strings.ContainsAny(uid, `/\\`) {
		return Location{}, errors.New("pod UID is invalid")
	}
	systemdUID := strings.ReplaceAll(uid, "-", "_")
	var matches []string
	err := filepath.WalkDir(r.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrPermission) {
				return fs.SkipDir
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() || path == r.root || !podDirectory(entry.Name(), uid, systemdUID) {
			return nil
		}
		if regularFile(filepath.Join(path, "cpu.stat")) && regularFile(filepath.Join(path, "cpu.weight")) {
			matches = append(matches, path)
		}
		return fs.SkipDir
	})
	if err != nil {
		return Location{}, fmt.Errorf("walk cgroup tree: %w", err)
	}
	if len(matches) == 0 {
		return Location{}, fmt.Errorf("%w for Pod UID %s", ErrPodCgroupNotFound, uid)
	}
	if len(matches) > 1 {
		sort.Strings(matches)
		return Location{}, fmt.Errorf("%w for Pod UID %s: %s", ErrAmbiguousCgroup, uid, strings.Join(matches, ", "))
	}
	return r.location(matches[0])
}

func (r *FSResolver) location(path string) (Location, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Location{}, fmt.Errorf("stat pod cgroup: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Ino == 0 {
		return Location{}, errors.New("pod cgroup inode is unavailable")
	}
	relative, err := filepath.Rel(r.root, path)
	if err != nil {
		return Location{}, fmt.Errorf("make cgroup path relative: %w", err)
	}
	return Location{Path: relative, ID: stat.Ino}, nil
}

func podDirectory(name, uid, systemdUID string) bool {
	lower := strings.ToLower(name)
	return lower == "pod"+uid ||
		lower == "pod"+systemdUID ||
		strings.HasSuffix(lower, "-pod"+systemdUID+".slice")
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

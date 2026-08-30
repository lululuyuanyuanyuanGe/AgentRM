package cgroup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePodSystemdLayout(t *testing.T) {
	root := t.TempDir()
	uid := "4f98b24d-27e1-4f42-a257-2d756d92c19b"
	group := filepath.Join(root, "kubepods.slice", "kubepods-burstable.slice", "kubepods-burstable-pod4f98b24d_27e1_4f42_a257_2d756d92c19b.slice")
	writeControllerFiles(t, group)
	writeControllerFiles(t, filepath.Join(group, "cri-containerd-deadbeef.scope"))

	resolver, _ := NewFSResolver(root)
	location, err := resolver.ResolvePod(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if location.Path != filepath.Join("kubepods.slice", "kubepods-burstable.slice", filepath.Base(group)) || location.ID == 0 {
		t.Fatalf("location = %#v", location)
	}
}

func TestResolvePodCgroupFSLayout(t *testing.T) {
	root := t.TempDir()
	uid := "4f98b24d-27e1-4f42-a257-2d756d92c19b"
	group := filepath.Join(root, "kubepods", "burstable", "pod"+uid)
	writeControllerFiles(t, group)

	resolver, _ := NewFSResolver(root)
	location, err := resolver.ResolvePod(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if location.Path != filepath.Join("kubepods", "burstable", "pod"+uid) {
		t.Fatalf("path = %q", location.Path)
	}
}

func TestResolvePodRejectsAmbiguousStaleCgroups(t *testing.T) {
	root := t.TempDir()
	uid := "4f98b24d-27e1-4f42-a257-2d756d92c19b"
	writeControllerFiles(t, filepath.Join(root, "one", "pod"+uid))
	writeControllerFiles(t, filepath.Join(root, "two", "pod"+uid))
	resolver, _ := NewFSResolver(root)
	if _, err := resolver.ResolvePod(context.Background(), uid); !errors.Is(err, ErrAmbiguousCgroup) {
		t.Fatalf("error = %v, want ErrAmbiguousCgroup", err)
	}
}

func writeControllerFiles(t *testing.T, group string) {
	t.Helper()
	if err := os.MkdirAll(group, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{"cpu.stat": "usage_usec 0\n", "cpu.weight": "100\n"} {
		if err := os.WriteFile(filepath.Join(group, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

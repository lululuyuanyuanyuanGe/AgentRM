package cgroup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCPUStat(t *testing.T) {
	stat, err := ParseCPUStat(strings.NewReader(`usage_usec 1200
user_usec 800
system_usec 400
nr_periods 22
nr_throttled 3
throttled_usec 50
extra_future_field 9
`))
	if err != nil {
		t.Fatal(err)
	}
	if stat.UsageUsec != 1200 || stat.UserUsec != 800 || stat.SystemUsec != 400 || stat.NRThrottled != 3 {
		t.Fatalf("unexpected stat: %+v", stat)
	}
}

func TestParseCPUStatRequiresUsage(t *testing.T) {
	if _, err := ParseCPUStat(strings.NewReader("user_usec 10\n")); err == nil {
		t.Fatal("expected missing usage_usec error")
	}
}

func TestFSClientReadsStatAndChangesOnlyWeight(t *testing.T) {
	root := t.TempDir()
	group := filepath.Join(root, "kubepods", "sandbox-a", "job-a")
	if err := os.MkdirAll(group, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(group, "cpu.stat"), "usage_usec 420\nuser_usec 300\nsystem_usec 120\n")
	writeTestFile(t, filepath.Join(group, "cpu.weight"), "100")
	writeTestFile(t, filepath.Join(group, "cpu.max"), "max 100000")

	client, err := NewFSClient(root)
	if err != nil {
		t.Fatal(err)
	}
	stat, err := client.ReadCPUStat(context.Background(), "kubepods/sandbox-a/job-a")
	if err != nil {
		t.Fatal(err)
	}
	if stat.UsageUsec != 420 {
		t.Fatalf("usage = %d, want 420", stat.UsageUsec)
	}
	if err := client.WriteWeight(context.Background(), "kubepods/sandbox-a/job-a", 3000); err != nil {
		t.Fatal(err)
	}
	weight, err := client.ReadWeight(context.Background(), "kubepods/sandbox-a/job-a")
	if err != nil || weight != 3000 {
		t.Fatalf("weight=%d err=%v", weight, err)
	}
	quota, err := os.ReadFile(filepath.Join(group, "cpu.max"))
	if err != nil {
		t.Fatal(err)
	}
	if string(quota) != "max 100000" {
		t.Fatalf("cpu.max changed: %q", quota)
	}
}

func TestFSClientRejectsEscapingPaths(t *testing.T) {
	client, err := NewFSClient(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside", "/sys/fs/cgroup/outside", "."} {
		if _, err := client.ReadWeight(context.Background(), path); err == nil {
			t.Fatalf("path %q should be rejected", path)
		}
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

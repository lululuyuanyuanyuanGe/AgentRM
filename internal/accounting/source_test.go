package accounting

import (
	"context"
	"testing"
	"time"
)

func TestMemorySourceKeepsKernelConfigurationSemantics(t *testing.T) {
	source := NewMemorySource()
	config := Configuration{CgroupID: 77, Level: LevelQ0, BudgetNS: uint64(4 * time.Second), Generation: 1}
	if err := source.Configure(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if err := source.Exhaust(77); err != nil {
		t.Fatal(err)
	}
	event := <-source.Events()
	if event.CgroupID != 77 || event.UsedNS != uint64(4*time.Second) || event.Generation != 1 {
		t.Fatalf("event = %#v", event)
	}
	if err := source.Remove(context.Background(), 77); err != nil {
		t.Fatal(err)
	}
	if _, ok := source.Configuration(77); ok {
		t.Fatal("removed cgroup remained configured")
	}
}

func TestAccountingConfigurationRejectsBudgetOnQ2(t *testing.T) {
	config := Configuration{CgroupID: 1, Level: LevelQ2, BudgetNS: 1, Generation: 1}
	if err := config.Validate(); err == nil {
		t.Fatal("expected Q2 finite budget to fail")
	}
}

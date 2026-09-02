package accounting

import (
	"encoding/binary"
	"testing"
)

func TestBPFMapAndRingBufferABI(t *testing.T) {
	if size := binary.Size(bpfEntityState{}); size != 32 {
		t.Fatalf("entity state size = %d, want 32", size)
	}
	if size := binary.Size(bpfThresholdEvent{}); size != 40 {
		t.Fatalf("threshold event size = %d, want 40", size)
	}
}

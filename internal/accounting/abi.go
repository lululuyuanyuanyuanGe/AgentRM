package accounting

// Keep these layouts in lockstep with bpf/agentrm.bpf.c. Fixed-width fields
// make the Ring Buffer ABI identical on amd64 and arm64 Linux nodes.
type bpfEntityState struct {
	UsedNS     uint64
	BudgetNS   uint64
	Level      uint32
	Generation uint32
	Reported   uint64
}

type bpfThresholdEvent struct {
	CgroupID    uint64
	UsedNS      uint64
	BudgetNS    uint64
	TimestampNS uint64
	Level       uint32
	Generation  uint32
}

package model

import (
	"errors"
	"fmt"
)

const MiB int64 = 1024 * 1024

// Resources stores CPU in millicores and memory in bytes. Integer units keep
// scheduling arithmetic deterministic and avoid floating-point drift.
type Resources struct {
	CPUMilli    int64 `json:"cpu_milli"`
	MemoryBytes int64 `json:"memory_bytes"`
}

func (r Resources) Validate() error {
	if r.CPUMilli < 0 {
		return errors.New("cpu_milli must be non-negative")
	}
	if r.MemoryBytes < 0 {
		return errors.New("memory_bytes must be non-negative")
	}
	return nil
}

func (r Resources) Add(other Resources) Resources {
	return Resources{CPUMilli: r.CPUMilli + other.CPUMilli, MemoryBytes: r.MemoryBytes + other.MemoryBytes}
}

func (r Resources) SubFloor(other Resources) Resources {
	return Resources{
		CPUMilli:    max(0, r.CPUMilli-other.CPUMilli),
		MemoryBytes: max(0, r.MemoryBytes-other.MemoryBytes),
	}
}

func (r Resources) Min(other Resources) Resources {
	return Resources{CPUMilli: min(r.CPUMilli, other.CPUMilli), MemoryBytes: min(r.MemoryBytes, other.MemoryBytes)}
}

func (r Resources) Max(other Resources) Resources {
	return Resources{CPUMilli: max(r.CPUMilli, other.CPUMilli), MemoryBytes: max(r.MemoryBytes, other.MemoryBytes)}
}

func (r Resources) Clamp(lower, upper Resources) Resources {
	return Resources{
		CPUMilli:    min(max(r.CPUMilli, lower.CPUMilli), upper.CPUMilli),
		MemoryBytes: min(max(r.MemoryBytes, lower.MemoryBytes), upper.MemoryBytes),
	}
}

func (r Resources) IsZero() bool {
	return r.CPUMilli == 0 && r.MemoryBytes == 0
}

func (r Resources) String() string {
	return fmt.Sprintf("cpu=%dm memory=%dMi", r.CPUMilli, r.MemoryBytes/MiB)
}

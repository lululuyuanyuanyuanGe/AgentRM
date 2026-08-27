package cgroup

import "context"

type CPUStat struct {
	UsageUsec     uint64 `json:"usage_usec"`
	UserUsec      uint64 `json:"user_usec"`
	SystemUsec    uint64 `json:"system_usec"`
	NRPeriods     uint64 `json:"nr_periods"`
	NRThrottled   uint64 `json:"nr_throttled"`
	ThrottledUsec uint64 `json:"throttled_usec"`
}

type Client interface {
	ReadCPUStat(context.Context, string) (CPUStat, error)
	ReadWeight(context.Context, string) (int, error)
	WriteWeight(context.Context, string, int) error
}

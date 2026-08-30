//go:build !linux

package accounting

import "errors"

func NewKernelSource(string) (Source, error) {
	return nil, errors.New("kernel eBPF accounting is only available on Linux")
}

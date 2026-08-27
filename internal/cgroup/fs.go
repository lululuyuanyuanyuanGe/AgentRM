package cgroup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type FSClient struct {
	root string
}

func NewFSClient(root string) (*FSClient, error) {
	if root == "" {
		return nil, errors.New("cgroup root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat cgroup root: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("cgroup root must be a directory")
	}
	return &FSClient{root: filepath.Clean(abs)}, nil
}

func (c *FSClient) ReadCPUStat(ctx context.Context, cgroupPath string) (CPUStat, error) {
	if err := ctx.Err(); err != nil {
		return CPUStat{}, err
	}
	path, err := c.resolve(cgroupPath, "cpu.stat")
	if err != nil {
		return CPUStat{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return CPUStat{}, err
	}
	defer file.Close()
	return ParseCPUStat(file)
}

func (c *FSClient) ReadWeight(ctx context.Context, cgroupPath string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	path, err := c.resolve(cgroupPath, "cpu.weight")
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	weight, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse cpu.weight: %w", err)
	}
	return weight, nil
}

func (c *FSClient) WriteWeight(ctx context.Context, cgroupPath string, weight int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if weight < 1 || weight > 10000 {
		return errors.New("cpu.weight must be between 1 and 10000")
	}
	path, err := c.resolve(cgroupPath, "cpu.weight")
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(weight)), 0o644)
}

func (c *FSClient) resolve(cgroupPath, file string) (string, error) {
	clean := filepath.Clean(cgroupPath)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("cgroup_path must be a relative path below the configured root")
	}
	resolved := filepath.Join(c.root, clean, file)
	if resolved != c.root && !strings.HasPrefix(resolved, c.root+string(filepath.Separator)) {
		return "", errors.New("resolved cgroup path escapes configured root")
	}
	return resolved, nil
}

func ParseCPUStat(reader io.Reader) (CPUStat, error) {
	var stat CPUStat
	foundUsage := false
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return CPUStat{}, fmt.Errorf("parse cpu.stat %s: %w", fields[0], err)
		}
		switch fields[0] {
		case "usage_usec":
			stat.UsageUsec = value
			foundUsage = true
		case "user_usec":
			stat.UserUsec = value
		case "system_usec":
			stat.SystemUsec = value
		case "nr_periods":
			stat.NRPeriods = value
		case "nr_throttled":
			stat.NRThrottled = value
		case "throttled_usec":
			stat.ThrottledUsec = value
		}
	}
	if err := scanner.Err(); err != nil {
		return CPUStat{}, err
	}
	if !foundUsage {
		return CPUStat{}, errors.New("cpu.stat does not contain usage_usec")
	}
	return stat, nil
}

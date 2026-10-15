package sysres

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// stats is one container's reading from docker stats.
type stats struct {
	name string
	cpu  float64
	mem  uint64
}

// parseStats reads one line of `docker stats --format '{{json .}}'`.
func parseStats(line string) (stats, error) {
	var raw struct {
		Name     string `json:"Name"`
		CPUPerc  string `json:"CPUPerc"`
		MemUsage string `json:"MemUsage"`
	}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return stats{}, fmt.Errorf("unreadable docker stats line: %w", err)
	}
	cpu, err := parsePercent(raw.CPUPerc)
	if err != nil {
		return stats{}, err
	}
	used, _, _ := strings.Cut(raw.MemUsage, "/")
	m, err := parseSize(used)
	if err != nil {
		return stats{}, err
	}
	return stats{name: raw.Name, cpu: cpu, mem: m}, nil
}

// parsePercent turns "0.53%" into 0.53.
func parsePercent(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "%"), 64)
	if err != nil {
		return 0, fmt.Errorf("unreadable CPU value %q", s)
	}
	return v, nil
}

// sizeUnits are the suffixes docker prints, longest first so "MiB" isn't read as "B".
var sizeUnits = []struct {
	suffix string
	mult   float64
}{
	{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
	{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"KB", 1e3},
	{"B", 1},
}

// parseSize turns "12.5MiB", "3kB" or "0B" into bytes.
func parseSize(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	for _, u := range sizeUnits {
		if num, ok := strings.CutSuffix(s, u.suffix); ok {
			v, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
			if err != nil || v < 0 {
				break
			}
			return uint64(v*u.mult + 0.5), nil
		}
	}
	return 0, fmt.Errorf("unreadable memory value %q", s)
}

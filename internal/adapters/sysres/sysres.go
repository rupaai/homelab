// Package sysres reports what the computer and the apps are using right now. It implements
// core.Resources. Nothing is stored; every call takes a fresh reading.
package sysres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/rupaai/homelab/internal/core"
)

// Labels the Docker adapter puts on every app container.
const (
	labelApp = "homelab.app"
	labelEnv = "homelab.env"
)

// cpuSample is how long Device watches the CPU to measure its load.
const cpuSample = 200 * time.Millisecond

// Runner runs a command and returns what it printed. Tests replace it so they don't need Docker.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Options configures a Reporter.
type Options struct {
	Logger *slog.Logger // default: discard
	Docker string       // default "docker"
	Runner Runner       // default: run the real command
}

// Reporter is the core.Resources implementation.
type Reporter struct {
	log    *slog.Logger
	docker string
	run    Runner
}

var _ core.Resources = (*Reporter)(nil)

// New returns a Reporter. It doesn't need Docker to be running yet.
func New(opts Options) *Reporter {
	r := &Reporter{log: opts.Logger, docker: opts.Docker, run: opts.Runner}
	if r.log == nil {
		r.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if r.docker == "" {
		r.docker = "docker"
	}
	if r.run == nil {
		r.run = execRunner
	}
	return r
}

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return out.Bytes(), fmt.Errorf("%w: %s", err, msg)
		}
		return out.Bytes(), err
	}
	return out.Bytes(), nil
}

// Device reports the computer's CPU, memory and the disk holding dataDir.
func (r *Reporter) Device(ctx context.Context, dataDir string) (core.DeviceUsage, error) {
	var u core.DeviceUsage
	pct, err := cpu.PercentWithContext(ctx, cpuSample, false)
	if err != nil || len(pct) == 0 {
		return u, errors.New("couldn't read how busy the computer is")
	}
	u.CPUPercent = pct[0]

	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return u, errors.New("couldn't read how much memory the computer is using")
	}
	u.MemTotal = vm.Total
	if vm.Available <= vm.Total {
		u.MemUsed = vm.Total - vm.Available
	}

	du, err := disk.UsageWithContext(ctx, dataDir)
	if err != nil {
		return u, fmt.Errorf("couldn't read how full the disk holding %s is", dataDir)
	}
	u.DiskUsed, u.DiskTotal = du.Used, du.Total
	return u, nil
}

// Apps reports each running app environment's CPU and memory, summed over its containers.
// Containers that aren't Homelab apps (such as supporting services) are left out.
func (r *Reporter) Apps(ctx context.Context) ([]core.AppUsage, error) {
	psOut, err := r.run(ctx, r.docker, "ps", "--format",
		`{{.Names}}`+"\t"+`{{.Label "`+labelApp+`"}}`+"\t"+`{{.Label "`+labelEnv+`"}}`)
	if err != nil {
		return nil, r.dockerDown(err)
	}
	statsOut, err := r.run(ctx, r.docker, "stats", "--no-stream", "--format", "{{json .}}")
	if err != nil {
		return nil, r.dockerDown(err)
	}

	owner := parseOwners(psOut)
	sums := map[[2]string]*core.AppUsage{}
	for _, line := range strings.Split(string(statsOut), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		s, err := parseStats(line)
		if err != nil {
			r.log.Warn("skipping a container whose usage couldn't be read", "err", err)
			continue
		}
		k, ok := owner[s.name]
		if !ok {
			continue
		}
		a := sums[k]
		if a == nil {
			a = &core.AppUsage{App: k[0], Env: k[1]}
			sums[k] = a
		}
		a.CPUPercent += s.cpu
		a.MemUsed += s.mem
	}

	out := make([]core.AppUsage, 0, len(sums))
	for _, a := range sums {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].App != out[j].App {
			return out[i].App < out[j].App
		}
		return out[i].Env < out[j].Env
	})
	return out, nil
}

func (r *Reporter) dockerDown(err error) error {
	r.log.Warn("docker did not answer", "err", err)
	return errors.New("Docker isn't answering, so app usage can't be shown right now. Check that Docker is running")
}

// parseOwners maps container names to (app, env) using the homelab.* labels. Containers
// without both labels are not Homelab apps and are left out.
func parseOwners(b []byte) map[string][2]string {
	m := map[string][2]string{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) != 3 || f[0] == "" || f[1] == "" || f[2] == "" {
			continue
		}
		m[f[0]] = [2]string{f[1], f[2]}
	}
	return m
}

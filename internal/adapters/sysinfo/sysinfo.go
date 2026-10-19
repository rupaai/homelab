// Package sysinfo describes the device: its OS, uptime, temperature, disks and pending OS
// updates (milestone 6). It implements core.System.
//
// Homelab doesn't run as root, so what needs root (disk health from smartctl, the count of
// pending updates) comes from a report that `homelabd system-report` writes as root every
// few minutes (a systemd timer that `homelabd setup` installs). Without the report those
// parts are just unknown.
package sysinfo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/sensors"

	"github.com/rupaai/homelab/internal/core"
)

// ReportPath is where the root report is written.
const ReportPath = "/run/homelab/system.json"

// Report is what the root report knows.
type Report struct {
	At              time.Time    `json:"at"`
	RebootRequired  bool         `json:"rebootRequired"`
	UpdatesPending  int          `json:"updatesPending"`  // -1 when unknown
	SecurityPending int          `json:"securityPending"` // -1 when unknown
	Disks           []DiskReport `json:"disks"`
}

// DiskReport is one physical disk's health and what is mounted from it.
type DiskReport struct {
	Device string   `json:"device"`
	Health string   `json:"health"` // ok | failing | "" (unknown)
	Mounts []string `json:"mounts"`
}

// Options configures a Reporter.
type Options struct {
	ReportPath string // default ReportPath
	// OSRelease is /etc/os-release; tests point it elsewhere.
	OSRelease string
	// MaxReportAge is how old a report may be and still be shown; default 1h.
	MaxReportAge time.Duration
	Now          func() time.Time
}

// Reporter is the core.System implementation.
type Reporter struct{ o Options }

var _ core.System = (*Reporter)(nil)

// New returns a Reporter.
func New(o Options) *Reporter {
	if o.ReportPath == "" {
		o.ReportPath = ReportPath
	}
	if o.OSRelease == "" {
		o.OSRelease = "/etc/os-release"
	}
	if o.MaxReportAge == 0 {
		o.MaxReportAge = time.Hour
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Reporter{o: o}
}

// Info describes the device right now.
func (r *Reporter) Info(ctx context.Context) (core.SystemInfo, error) {
	s := core.SystemInfo{Disks: []core.DiskInfo{}, UpdatesPending: -1, SecurityPending: -1}
	if h, err := host.InfoWithContext(ctx); err == nil {
		s.Hostname, s.Kernel, s.UptimeSeconds = h.Hostname, h.KernelVersion, h.Uptime
		s.OS = strings.TrimSpace(h.Platform + " " + h.PlatformVersion)
		if runtime.GOOS == "darwin" {
			s.OS = "macOS " + h.PlatformVersion
		}
	}
	if name := prettyName(r.o.OSRelease); name != "" {
		s.OS = name
	}
	if temps, err := sensors.TemperaturesWithContext(ctx); err == nil || len(temps) > 0 {
		for _, t := range temps {
			if t.Temperature > s.TemperatureC && t.Temperature < 130 {
				s.TemperatureC = t.Temperature
			}
		}
	}
	rep, haveReport := r.report()
	if haveReport {
		at := rep.At
		s.ReportedAt = &at
		s.RebootRequired = rep.RebootRequired
		s.UpdatesPending, s.SecurityPending = rep.UpdatesPending, rep.SecurityPending
	} else if _, err := os.Stat("/var/run/reboot-required"); err == nil {
		s.RebootRequired = true
	}
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return s, fmt.Errorf("couldn't list the disks: %w", err)
	}
	seen := map[string]bool{}
	for _, p := range parts {
		if !shownMount(p.Mountpoint, p.Fstype) || seen[p.Device] {
			continue
		}
		u, err := disk.UsageWithContext(ctx, p.Mountpoint)
		if err != nil || u.Total < 1<<30 {
			continue
		}
		seen[p.Device] = true
		d := core.DiskInfo{Mount: p.Mountpoint, Device: p.Device, Used: u.Used, Total: u.Total}
		if haveReport {
			d.Health = healthOf(rep, p.Mountpoint)
		}
		s.Disks = append(s.Disks, d)
	}
	sort.Slice(s.Disks, func(i, j int) bool { return s.Disks[i].Mount < s.Disks[j].Mount })
	return s, nil
}

// report reads the root report if it is there and fresh.
func (r *Reporter) report() (Report, bool) {
	var rep Report
	b, err := os.ReadFile(r.o.ReportPath)
	if err != nil || json.Unmarshal(b, &rep) != nil {
		return rep, false
	}
	if r.o.Now().Sub(rep.At) > r.o.MaxReportAge {
		return rep, false
	}
	return rep, true
}

func healthOf(rep Report, mount string) string {
	for _, d := range rep.Disks {
		for _, m := range d.Mounts {
			if m == mount {
				return d.Health
			}
		}
	}
	return ""
}

// shownMount leaves out system and container mounts that aren't disks people think about.
func shownMount(mount, fstype string) bool {
	switch fstype {
	case "squashfs", "overlay", "tmpfs", "devtmpfs", "autofs", "vfat", "nsfs", "fuse.lxcfs":
		return false
	}
	for _, p := range []string{"/boot", "/snap", "/var/lib/docker", "/run", "/System/Volumes", "/Library/Developer", "/private/var"} {
		if mount == p || strings.HasPrefix(mount, p+"/") {
			return false
		}
	}
	return true
}

// prettyName reads PRETTY_NAME from an os-release file.
func prettyName(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// Runner runs a command and returns its stdout and stderr. Tests replace it.
type Runner func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

// BuildReport gathers the root-only facts. run may be nil for the real commands.
func BuildReport(ctx context.Context, run Runner, now time.Time) Report {
	if run == nil {
		run = execRunner
	}
	rep := Report{At: now, UpdatesPending: -1, SecurityPending: -1, Disks: []DiskReport{}}
	if _, err := os.Stat("/var/run/reboot-required"); err == nil {
		rep.RebootRequired = true
	}
	// apt-check prints "<updates>;<security updates>" on stderr.
	if _, errOut, err := run(ctx, "/usr/lib/update-notifier/apt-check"); err == nil {
		rep.UpdatesPending, rep.SecurityPending = parseAptCheck(string(errOut))
	}
	out, _, err := run(ctx, "lsblk", "-J", "-p", "-o", "NAME,TYPE,MOUNTPOINTS")
	if err != nil {
		return rep
	}
	disks, err := parseLsblk(out)
	if err != nil {
		return rep
	}
	for _, d := range disks {
		smart, _, err := run(ctx, "smartctl", "-H", "-j", d.Device)
		// smartctl uses its exit code as a bit field, so read the answer even on "errors".
		if len(smart) > 0 || err == nil {
			d.Health = parseSmart(smart)
		}
		rep.Disks = append(rep.Disks, d)
	}
	return rep
}

// WriteReport writes the report where Homelab reads it, readable by everyone.
func WriteReport(path string, rep Report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func parseAptCheck(s string) (int, int) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), ";")
	if !ok {
		return -1, -1
	}
	n, err1 := strconv.Atoi(a)
	m, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil {
		return -1, -1
	}
	return n, m
}

type lsblkDev struct {
	Name        string     `json:"name"`
	Type        string     `json:"type"`
	Mountpoints []*string  `json:"mountpoints"`
	Children    []lsblkDev `json:"children"`
}

// parseLsblk lists the physical disks and every mount point in each one's tree.
func parseLsblk(b []byte) ([]DiskReport, error) {
	var doc struct {
		Devices []lsblkDev `json:"blockdevices"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	var out []DiskReport
	for _, d := range doc.Devices {
		if d.Type != "disk" || strings.Contains(d.Name, "/zram") {
			continue
		}
		r := DiskReport{Device: d.Name, Mounts: []string{}}
		var walk func(lsblkDev)
		walk = func(x lsblkDev) {
			for _, m := range x.Mountpoints {
				if m != nil && *m != "" {
					r.Mounts = append(r.Mounts, *m)
				}
			}
			for _, c := range x.Children {
				walk(c)
			}
		}
		walk(d)
		out = append(out, r)
	}
	if out == nil {
		return nil, errors.New("no disks")
	}
	return out, nil
}

// parseSmart reads `smartctl -H -j`.
func parseSmart(b []byte) string {
	var s struct {
		SmartStatus *struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
	}
	if json.Unmarshal(b, &s) != nil || s.SmartStatus == nil {
		return ""
	}
	if s.SmartStatus.Passed {
		return "ok"
	}
	return "failing"
}

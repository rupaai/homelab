package sysinfo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const lsblkOut = `{"blockdevices": [
 {"name":"/dev/sda","type":"disk","mountpoints":[null],"children":[
   {"name":"/dev/sda1","type":"part","mountpoints":["/boot/efi"]},
   {"name":"/dev/sda3","type":"part","mountpoints":[null],"children":[
     {"name":"/dev/mapper/ubuntu--vg-ubuntu--lv","type":"lvm","mountpoints":["/"]}]}]},
 {"name":"/dev/sdb","type":"disk","mountpoints":[null],"children":[
   {"name":"/dev/sdb1","type":"part","mountpoints":["/mnt/storage"]}]},
 {"name":"/dev/loop0","type":"loop","mountpoints":["/snap/core/1"]}
]}`

func TestBuildReport(t *testing.T) {
	run := func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		switch name {
		case "/usr/lib/update-notifier/apt-check":
			return nil, []byte("12;3"), nil
		case "lsblk":
			return []byte(lsblkOut), nil, nil
		case "smartctl":
			if args[len(args)-1] == "/dev/sdb" {
				// exit status 8 (failing disk) still prints the answer
				return []byte(`{"smart_status":{"passed":false}}`), nil, errors.New("exit status 8")
			}
			return []byte(`{"smart_status":{"passed":true}}`), nil, nil
		}
		return nil, nil, errors.New("unexpected " + name)
	}
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	rep := BuildReport(context.Background(), run, now)
	if rep.UpdatesPending != 12 || rep.SecurityPending != 3 || len(rep.Disks) != 2 {
		t.Fatalf("report = %+v", rep)
	}
	if rep.Disks[0].Health != "ok" || healthOf(rep, "/") != "ok" || healthOf(rep, "/mnt/storage") != "failing" || healthOf(rep, "/x") != "" {
		t.Fatalf("disks = %+v", rep.Disks)
	}

	path := filepath.Join(t.TempDir(), "homelab", "system.json")
	if err := WriteReport(path, rep); err != nil {
		t.Fatal(err)
	}
	r := New(Options{ReportPath: path, Now: func() time.Time { return now.Add(10 * time.Minute) }})
	if got, ok := r.report(); !ok || got.UpdatesPending != 12 {
		t.Fatalf("read back = %+v %v", got, ok)
	}
	r.o.Now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, ok := r.report(); ok {
		t.Fatal("an old report was used")
	}
}

func TestBuildReportWithoutTools(t *testing.T) {
	run := func(context.Context, string, ...string) ([]byte, []byte, error) {
		return nil, nil, errors.New("not found")
	}
	rep := BuildReport(context.Background(), run, time.Now())
	if rep.UpdatesPending != -1 || rep.SecurityPending != -1 || len(rep.Disks) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestParsers(t *testing.T) {
	if a, b := parseAptCheck("nonsense"); a != -1 || b != -1 {
		t.Error("bad apt-check output was read")
	}
	if parseSmart([]byte(`{}`)) != "" {
		t.Error("smartctl without a status read as known")
	}
	f := filepath.Join(t.TempDir(), "os-release")
	os.WriteFile(f, []byte("NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 24.04.4 LTS\"\n"), 0o644)
	if prettyName(f) != "Ubuntu 24.04.4 LTS" {
		t.Error("PRETTY_NAME not read")
	}
	if shownMount("/boot/efi", "vfat") || shownMount("/snap/x", "squashfs") || !shownMount("/mnt/storage", "ext4") || !shownMount("/", "ext4") {
		t.Error("shownMount")
	}
}

func TestInfoRuns(t *testing.T) {
	s, err := New(Options{ReportPath: filepath.Join(t.TempDir(), "none")}).Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.UpdatesPending != -1 || s.ReportedAt != nil {
		t.Fatalf("info without a report = %+v", s)
	}
}

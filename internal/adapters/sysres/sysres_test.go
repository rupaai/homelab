package sysres

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	cases := map[string]uint64{
		"0B":      0,
		"512B":    512,
		"3kB":     3000,
		"12.5MiB": 13107200,
		"1GiB":    1 << 30,
		"7.6GiB":  8160437862,
	}
	for in, want := range cases {
		got, err := parseSize(in)
		if err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := parseSize("lots"); err == nil {
		t.Error("expected an error for nonsense")
	}
}

func TestParseStats(t *testing.T) {
	s, err := parseStats(`{"Name":"x","CPUPerc":"0.53%","MemUsage":"12.5MiB / 7.6GiB"}`)
	if err != nil || s.name != "x" || s.cpu != 0.53 || s.mem != 13107200 {
		t.Fatalf("got %+v, %v", s, err)
	}
}

func fake(ps, stats string, err error) Runner {
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if err != nil {
			return nil, err
		}
		if args[0] == "ps" {
			return []byte(ps), nil
		}
		return []byte(stats), nil
	}
}

const psOut = "homelab-movies-dev-app-1\tmovies\tdev\n" +
	"homelab-movies-dev-sidecar-1\tmovies\tdev\n" +
	"homelab-movies-prod-app-1\tmovies\tprod\n" +
	"homelab-pocketid-pocketid-1\t\t\n"

const statsOut = `{"Name":"homelab-movies-dev-app-1","CPUPerc":"1.50%","MemUsage":"100MiB / 7.6GiB"}
{"Name":"homelab-movies-dev-sidecar-1","CPUPerc":"0.50%","MemUsage":"512kB / 7.6GiB"}
{"Name":"homelab-movies-prod-app-1","CPUPerc":"0.00%","MemUsage":"1GiB / 7.6GiB"}
{"Name":"homelab-pocketid-pocketid-1","CPUPerc":"9.00%","MemUsage":"50MiB / 7.6GiB"}
{"Name":"unrelated","CPUPerc":"3.00%","MemUsage":"10B / 7.6GiB"}
`

func TestAppsSumsAndSkips(t *testing.T) {
	r := New(Options{Runner: fake(psOut, statsOut, nil)})
	got, err := r.Apps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 app environments, got %+v", got)
	}
	dev, prod := got[0], got[1]
	if dev.App != "movies" || dev.Env != "dev" || dev.CPUPercent != 2.0 || dev.MemUsed != 100<<20+512000 {
		t.Errorf("dev = %+v", dev)
	}
	if prod.Env != "prod" || prod.MemUsed != 1<<30 {
		t.Errorf("prod = %+v", prod)
	}
}

func TestAppsDockerDown(t *testing.T) {
	r := New(Options{Runner: fake("", "", errors.New("exit status 1"))})
	_, err := r.Apps(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Docker isn't answering") {
		t.Fatalf("got %v", err)
	}
}

func TestDevice(t *testing.T) {
	u, err := New(Options{}).Device(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if u.MemTotal == 0 || u.DiskTotal == 0 {
		t.Errorf("totals should be above zero: %+v", u)
	}
}

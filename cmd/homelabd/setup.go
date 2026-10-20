package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/adapters/selfupdate"
	"github.com/rupaai/homelab/internal/adapters/services"
	"github.com/rupaai/homelab/internal/adapters/sysinfo"
	"github.com/rupaai/homelab/internal/sharedir"
	"gopkg.in/yaml.v3"
)

// Where `homelabd setup` installs Homelab on a Linux device (milestone 6).
const (
	deviceHome    = "/var/lib/homelab"
	deviceUser    = "homelab"
	helperDir     = "/usr/local/lib/homelab" // a copy of the program for the update guard and the report
	unitDir       = "/etc/systemd/system"
	aptConfigFile = "/etc/apt/apt.conf.d/52homelab-unattended-upgrades"
)

// unitParams are what the systemd units depend on.
type unitParams struct {
	Home, User, Helper string
}

func homelabUnit(p unitParams) string {
	return fmt.Sprintf(`# Written by homelabd setup.
[Unit]
Description=Homelab
Wants=network-online.target docker.service
After=network-online.target docker.service
StartLimitIntervalSec=0

[Service]
User=%[2]s
Group=%[2]s
SupplementaryGroups=docker
Environment=HOME=%[1]s
WorkingDirectory=%[1]s
# Counts starts of a new version and goes back to the previous one if it keeps failing.
ExecStartPre=-%[3]s update-guard --home %[1]s
ExecStart=%[1]s/bin/homelabd run --home %[1]s
Restart=always
RestartSec=5
UMask=0077
NoNewPrivileges=yes
ProtectSystem=full
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
`, p.Home, p.User, p.Helper)
}

func reportService(p unitParams) string {
	return fmt.Sprintf(`# Written by homelabd setup. Runs as root: disk health and pending OS updates need it.
[Unit]
Description=Homelab device report (disk health, OS updates)

[Service]
Type=oneshot
ExecStart=%s system-report
`, p.Helper)
}

const reportTimer = `# Written by homelabd setup.
[Unit]
Description=Homelab device report every 15 minutes

[Timer]
OnBootSec=1min
OnUnitActiveSec=15min

[Install]
WantedBy=timers.target
`

// aptConfig turns on Ubuntu's automatic security updates. It never reboots on its own;
// the companion says when a restart is needed.
const aptConfig = `// Written by homelabd setup: install security updates every day.
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
`

const deviceConfig = `# Homelab settings. See docs/milestone6.md.

# Each app environment gets its own Tailscale address.
addresses: tailscale

tailscale:
  tags: [tag:homelab]

# The companion on the device itself. People use the Tailscale address instead.
listen: 127.0.0.1:7080
`

func cmdSetup(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	importFlag := flags.String("import", "", "a move file made by `homelabd export` on the old computer")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if runtime.GOOS != "linux" {
		return errors.New("setup installs Homelab on a Linux device; on a Mac use `homelabd service install`")
	}
	if os.Geteuid() != 0 {
		return errors.New("run setup with sudo")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("this device has no systemd, which setup needs")
	}
	step := func(f string, a ...any) { fmt.Fprintf(out, "• "+f+"\n", a...) }
	home := deviceHome
	p := unitParams{Home: home, User: deviceUser, Helper: filepath.Join(helperDir, "homelabd")}

	if err := checkDocker(out, step); err != nil {
		return err
	}
	if err := installPackages(out, step); err != nil {
		return err
	}
	if err := ensureUser(step); err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return err
	}
	fresh := false
	if *importFlag != "" {
		if _, err := os.Stat(filepath.Join(home, "homelab.db")); err == nil {
			return fmt.Errorf("%s already has a Homelab, so the move file wasn't imported. Run setup without --import to just update it", home)
		}
		_ = sh(io.Discard, "systemctl", "stop", "homelab")
		n, err := extractArchive(*importFlag, home)
		if err != nil {
			return err
		}
		step("Imported %d files from %s", n, *importFlag)
		if err := useTailscaleAddresses(filepath.Join(home, "config.yaml")); err != nil {
			return err
		}
		fresh = true
	}
	if _, err := os.Stat(filepath.Join(home, "config.yaml")); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(deviceConfig), 0o600); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(home, "secrets"), 0o700); err != nil {
			return err
		}
		step("Wrote %s", filepath.Join(home, "config.yaml"))
		fresh = true
	}
	if fresh {
		// Imported app data belongs to root now; open it to the app containers (see sharedir).
		for _, pattern := range []string{"apps/*/*/data", "services/*/data"} {
			dirs, _ := filepath.Glob(filepath.Join(home, pattern))
			for _, d := range dirs {
				if err := sharedir.Open(d); err != nil {
					return fmt.Errorf("open %s to its app: %w", d, err)
				}
			}
		}
		if err := sh(out, "chown", "-R", deviceUser+":"+deviceUser, home); err != nil {
			return err
		}
	}

	version, err := installProgram(home, p.Helper)
	if err != nil {
		return err
	}
	step("Installed Homelab %s", version)

	files := map[string]string{
		filepath.Join(unitDir, "homelab.service"):        homelabUnit(p),
		filepath.Join(unitDir, "homelab-report.service"): reportService(p),
		filepath.Join(unitDir, "homelab-report.timer"):   reportTimer,
		aptConfigFile: aptConfig,
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	step("Turned on automatic security updates (no automatic restarts)")
	for _, c := range [][]string{
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", "--now", "homelab-report.timer"},
		{"systemctl", "enable", "homelab"},
		{"systemctl", "restart", "homelab"},
	} {
		if err := sh(out, c[0], c[1:]...); err != nil {
			return err
		}
	}
	// Write the first report now so the companion has it straight away.
	_ = sh(io.Discard, "systemctl", "start", "homelab-report.service")
	step("Started Homelab")
	fmt.Fprintf(out, `
Homelab is starting. Watch it with:
  sudo journalctl -u homelab -f
When it prints "Companion (Tailscale): https://...", open that address on your phone or Mac.
`)
	return nil
}

// useTailscaleAddresses switches an imported config to Tailscale addresses: on the device,
// local addresses would only work on the device itself.
func useTailscaleAddresses(path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cfg, err := parseConfig(b)
	if err != nil {
		return err
	}
	if cfg.Addresses == "tailscale" {
		return nil
	}
	cfg.Addresses = "tailscale"
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append([]byte("# Switched to Tailscale addresses by homelabd setup.\n"), out...), 0o600)
}

// checkDocker makes sure Docker and its compose plugin are there.
func checkDocker(out io.Writer, step func(string, ...any)) error {
	if err := sh(io.Discard, "docker", "version", "--format", "{{.Server.Version}}"); err != nil {
		return errors.New("Docker isn't running on this device. Install it first: https://docs.docker.com/engine/install/ubuntu/")
	}
	if sh(io.Discard, "docker", "compose", "version") == nil {
		return nil
	}
	step("Installing the Docker compose plugin")
	for _, pkg := range []string{"docker-compose-v2", "docker-compose-plugin"} {
		if aptInstall(out, pkg) == nil && sh(io.Discard, "docker", "compose", "version") == nil {
			return nil
		}
	}
	return errors.New("couldn't install the Docker compose plugin; install it and run setup again")
}

// installPackages installs what Homelab needs that the device lacks.
func installPackages(out io.Writer, step func(string, ...any)) error {
	need := map[string]string{"git": "git", "smartctl": "smartmontools", "unattended-upgrade": "unattended-upgrades"}
	var missing []string
	for bin, pkg := range need {
		if _, err := exec.LookPath(bin); err != nil {
			missing = append(missing, pkg)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	step("Installing %s", strings.Join(missing, ", "))
	_ = sh(out, "apt-get", "update")
	return aptInstall(out, missing...)
}

func aptInstall(out io.Writer, pkgs ...string) error {
	cmd := exec.Command("apt-get", append([]string{"install", "-y"}, pkgs...)...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}

// ensureUser makes the homelab system account. Only it uses Docker for Homelab; people's
// own accounts don't need to be in the docker group.
func ensureUser(step func(string, ...any)) error {
	if sh(io.Discard, "id", "-u", deviceUser) != nil {
		// A homelab group may already be there (made by hand); use it rather than fail.
		group := []string{"--user-group"}
		if sh(io.Discard, "getent", "group", deviceUser) == nil {
			group = []string{"-g", deviceUser}
		}
		args := append([]string{"--system"}, group...)
		args = append(args, "--home-dir", deviceHome, "--no-create-home", "--shell", "/usr/sbin/nologin", deviceUser)
		if err := sh(os.Stderr, "useradd", args...); err != nil {
			return err
		}
		step("Made the %s system account", deviceUser)
	}
	return sh(os.Stderr, "usermod", "-aG", "docker", deviceUser)
}

// installProgram copies this program into Homelab's bin folder, makes it the one that
// runs, and keeps a copy for the update guard and the device report.
func installProgram(home, helper string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return "", err
	}
	bin := selfupdate.BinDir(home)
	if _, err := selfupdate.Install(bin, version, data); err != nil {
		return "", err
	}
	// homelab-services comes along when it sits next to this program, as in a release.
	if svc, err := os.ReadFile(services.Program(exe)); err == nil {
		if _, err := selfupdate.InstallServices(bin, version, svc); err != nil {
			return "", err
		}
	}
	if err := selfupdate.Activate(bin, version); err != nil {
		return "", err
	}
	if err := sh(io.Discard, "chown", "-R", deviceUser+":"+deviceUser, bin); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(helper), 0o755); err != nil {
		return "", err
	}
	tmp := helper + ".tmp"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return "", err
	}
	return version, os.Rename(tmp, helper)
}

// sh runs a command, sending its output to out.
func sh(out io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// cmdUpdateGuard runs before every start (see selfupdate.Guard). It never fails the start.
func cmdUpdateGuard(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("update-guard", flag.ContinueOnError)
	homeFlag := flags.String("home", deviceHome, "Homelab folder")
	if err := flags.Parse(args); err != nil {
		return err
	}
	msg, err := selfupdate.Guard(selfupdate.BinDir(*homeFlag), time.Now())
	if err != nil {
		fmt.Fprintln(out, "update guard:", err)
	}
	if msg != "" {
		fmt.Fprintln(out, "update guard:", msg)
	}
	return nil
}

// cmdSystemReport writes the root-only device facts for the companion (see sysinfo).
func cmdSystemReport(args []string) error {
	flags := flag.NewFlagSet("system-report", flag.ContinueOnError)
	path := flags.String("out", sysinfo.ReportPath, "where to write the report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return sysinfo.WriteReport(*path, sysinfo.BuildReport(ctx, nil, time.Now()))
}

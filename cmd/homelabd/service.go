package main

import (
	"bytes"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const launchdLabel = "net.homelab.homelabd"

// plistParams are what the launchd file depends on.
type plistParams struct {
	Exe     string // absolute path of homelabd
	Home    string // Homelab folder
	UserDir string // the user's home folder, for ~/.orbstack/bin
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// launchdPlist renders the LaunchAgent file.
func launchdPlist(p plistParams) string {
	path := strings.Join([]string{
		"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin",
		filepath.Join(p.UserDir, ".orbstack", "bin"),
	}, ":")
	log := filepath.Join(p.Home, "logs", "homelabd.log")
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>run</string>
		<string>--home</string>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>%s</string>
	</dict>
</dict>
</plist>
`, launchdLabel, esc(p.Exe), esc(p.Home), esc(log), esc(log), esc(path))
}

func plistPath() (string, error) {
	uh, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(uh, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

func cmdService(args []string, out io.Writer) error {
	if len(args) == 0 || (args[0] != "install" && args[0] != "uninstall") {
		return fmt.Errorf("usage: homelabd service install|uninstall")
	}
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(out, "Running Homelab as a service is only set up for macOS in milestone 1. Milestone 6 adds the Linux service.")
		return nil
	}
	path, err := plistPath()
	if err != nil {
		return err
	}
	if args[0] == "uninstall" {
		fmt.Fprintf(out, "Run this to stop the service:\n  launchctl bootout gui/$(id -u) %s\n", path)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Fprintln(out, "Removed", path)
		return nil
	}

	fs := flag.NewFlagSet("service install", flag.ContinueOnError)
	homeFlag := fs.String("home", "", "Homelab folder (default ~/.homelab)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	home, err := resolveHome(*homeFlag)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	uh, _ := os.UserHomeDir()
	if err := os.MkdirAll(filepath.Join(home, "logs"), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(launchdPlist(plistParams{Exe: exe, Home: home, UserDir: uh})), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "Wrote %s\nTo start Homelab now and at every login, run:\n  launchctl bootstrap gui/$(id -u) %s\nLogs: %s\n",
		path, path, filepath.Join(home, "logs", "homelabd.log"))
	return nil
}

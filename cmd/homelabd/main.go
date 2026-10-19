// Command homelabd is the Homelab program: it sets up its folder, runs the hub and
// installs itself as a login service.
package main

import (
	"fmt"
	"os"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `Homelab

Usage:
  homelabd init [--home DIR]               create the Homelab folder and a starter config
  homelabd run [--home DIR] [--log-level L] run Homelab
  homelabd service install [--home DIR]    run Homelab at login (macOS)
  homelabd service uninstall               stop running Homelab at login (macOS)
  homelabd recover [--home DIR]             print a 15-minute owner sign-in link
  homelabd export [--home DIR] [--out FILE] pack this Homelab up to move it to another computer
  sudo homelabd setup [--import FILE]       install Homelab on a Linux device (milestone 6)
  homelabd version
`

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "homelabd:", err)
		os.Exit(1)
	}
}

func dispatch(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("say what to do: init, run, service, export, setup or version")
	}
	rest := args[1:]
	switch args[0] {
	case "init":
		return cmdInit(rest, os.Stdout)
	case "run":
		return cmdRun(rest)
	case "export":
		return cmdExport(rest, os.Stdout)
	case "setup":
		return cmdSetup(rest, os.Stdout)
	case "update-guard":
		return cmdUpdateGuard(rest, os.Stdout)
	case "system-report":
		return cmdSystemReport(rest)
	case "recover":
		return cmdRecover(rest, os.Stdout)
	case "service":
		return cmdService(rest, os.Stdout)
	case "version", "--version", "-version":
		fmt.Println("homelabd", version)
		return nil
	case "help", "-h", "--help", "-help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

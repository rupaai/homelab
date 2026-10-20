// Command homelabd is the Homelab program: it installs itself on a Linux device and runs the hub.
package main

import (
	"fmt"
	"os"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `Homelab

Usage:
  homelabd run [--home DIR] [--log-level L] run Homelab (needs DIR/config.yaml; it can be empty)
  homelabd recover [--home DIR]             print a 15-minute owner sign-in link
  homelabd password [--home DIR]            set the password for signing in from other devices (addresses: ip)
  sudo homelabd setup [--force]             install Homelab on a Linux device (milestone 6)
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
		return fmt.Errorf("say what to do: run, setup, password, recover or version")
	}
	rest := args[1:]
	switch args[0] {
	case "run":
		return cmdRun(rest)
	case "setup":
		return cmdSetup(rest, os.Stdout)
	case "update-guard":
		return cmdUpdateGuard(rest, os.Stdout)
	case "system-report":
		return cmdSystemReport(rest)
	case "recover":
		return cmdRecover(rest, os.Stdout)
	case "password":
		return cmdPassword(rest, os.Stdin, os.Stdout)
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

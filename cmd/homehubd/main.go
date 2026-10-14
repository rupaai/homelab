// Command homehubd is the Homehub program: it sets up its folder, runs the hub and
// installs itself as a login service.
package main

import (
	"fmt"
	"os"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `Homehub

Usage:
  homehubd init [--home DIR]               create the Homehub folder and a starter config
  homehubd run [--home DIR] [--log-level L] run Homehub
  homehubd service install [--home DIR]    run Homehub at login (macOS)
  homehubd service uninstall               stop running Homehub at login (macOS)
  homehubd version
`

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "homehubd:", err)
		os.Exit(1)
	}
}

func dispatch(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("say what to do: init, run, service or version")
	}
	rest := args[1:]
	switch args[0] {
	case "init":
		return cmdInit(rest, os.Stdout)
	case "run":
		return cmdRun(rest)
	case "service":
		return cmdService(rest, os.Stdout)
	case "version", "--version", "-version":
		fmt.Println("homehubd", version)
		return nil
	case "help", "-h", "--help", "-help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

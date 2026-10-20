package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

// passwordFile holds the bcrypt hash of the owner's password for addresses: ip.
func passwordFile(home string) string { return filepath.Join(home, "secrets", "companion-password") }

// minPassword is the shortest password Homelab accepts; it guards the whole companion.
const minPassword = 10

// cmdPassword sets the owner's password for signing in from other devices (addresses: ip).
func cmdPassword(args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("password", flag.ContinueOnError)
	homeFlag := flags.String("home", "", "Homelab folder (default ~/.homelab; /var/lib/homelab on a device)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	home, err := resolveHome(*homeFlag)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(home, "config.yaml")); err != nil {
		return fmt.Errorf("no Homelab at %s (on the device, add --home /var/lib/homelab)", home)
	}
	read := passwordReader(in, out)
	first, err := read("New password: ")
	if err != nil {
		return err
	}
	if len([]rune(first)) < minPassword {
		return fmt.Errorf("use at least %d characters", minPassword)
	}
	again, err := read("Same password again: ")
	if err != nil {
		return err
	}
	if first != again {
		return errors.New("the two passwords are different; nothing was changed")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(first), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	path := passwordFile(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(hash, '\n'), 0o600); err != nil {
		return err
	}
	// Run with sudo on the device, the file must still belong to the account Homelab runs as.
	if st, err := os.Stat(filepath.Dir(path)); err == nil {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 {
			if err := os.Chown(tmp, int(sys.Uid), int(sys.Gid)); err != nil {
				os.Remove(tmp)
				return err
			}
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	fmt.Fprintln(out, "Password set. Sign in on other devices at http://<this computer's IP>:7080 (needs addresses: ip in config.yaml).")
	return nil
}

// passwordReader reads one line per prompt, without showing it when typed in a terminal.
func passwordReader(in io.Reader, out io.Writer) func(prompt string) (string, error) {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return func(prompt string) (string, error) {
			fmt.Fprint(out, prompt)
			b, err := term.ReadPassword(int(f.Fd()))
			fmt.Fprintln(out)
			return string(b), err
		}
	}
	br := bufio.NewReader(in)
	return func(prompt string) (string, error) {
		fmt.Fprint(out, prompt)
		line, err := br.ReadString('\n')
		if err != nil && !(errors.Is(err, io.EOF) && line != "") {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
}

package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/adapters/secretstore"
)

// movedFile marks a Homelab folder whose Homelab now runs on another computer.
const movedFile = "MOVED"

// skipOnMove are the top-level parts of the Homelab folder a move leaves behind: this
// computer's Tailscale machines, mirrors (fetched again), logs, local addresses, the
// installed program and the moved marker.
var skipOnMove = map[string]bool{
	"tailscale": true, "mirrors": true, "logs": true, "local": true, "bin": true, movedFile: true,
}

func cmdExport(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("export", flag.ContinueOnError)
	homeFlag := flags.String("home", "", "Homelab folder (default ~/.homelab)")
	outFlag := flags.String("out", "", "file to write (default ~/homelab-move-<date>.tar.gz)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	home, err := resolveHome(*homeFlag)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(home)
	if err != nil {
		return err
	}
	if err := checkNotMoved(home); err != nil {
		return err
	}
	if err := checkStopped(cfg.Listen); err != nil {
		return err
	}
	dest := *outFlag
	if dest == "" {
		uh, _ := os.UserHomeDir()
		dest = filepath.Join(uh, "homelab-move-"+time.Now().Format("2006-01-02")+".tar.gz")
	}
	n, err := writeArchive(home, dest)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Wrote %s (%d files). It holds every secret, so keep it private and delete it after the move.\n", dest, n)

	if cfg.Addresses == "tailscale" {
		removeTailscaleMachines(home, cfg, out)
	}
	removeContainers(out)
	note := fmt.Sprintf("Homelab moved to another computer on %s (%s).\nTo run it here again, delete this file.\n", time.Now().Format(time.RFC1123), dest)
	if err := os.WriteFile(filepath.Join(home, movedFile), []byte(note), 0o644); err != nil {
		return err
	}
	if plist, err := plistPath(); err == nil {
		if _, err := os.Stat(plist); err == nil {
			fmt.Fprintf(out, "Homelab is set to start at login here. Turn that off with: homelabd service uninstall\n")
		}
	}
	fmt.Fprintf(out, `
Homelab won't start on this computer any more (delete %s to undo).
Next, copy the file to the device and install Homelab there with it:
  scp %s <you>@<device>:
  then on the device: sudo ./homelabd-linux-amd64 setup --import ~/%s
`, filepath.Join(home, movedFile), dest, filepath.Base(dest))
	return nil
}

// checkNotMoved refuses to run a Homelab that has moved, so two copies never run at once.
func checkNotMoved(home string) error {
	if b, err := os.ReadFile(filepath.Join(home, movedFile)); err == nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(b))+" ("+filepath.Join(home, movedFile)+")")
	}
	return nil
}

// checkStopped makes sure Homelab isn't running, so its database is copied whole.
func checkStopped(listen string) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("Homelab is still running here (%s is taken). Stop it first: Ctrl+C in its Terminal window, or "+
			"`launchctl bootout gui/$(id -u) ~/Library/LaunchAgents/%s.plist` if it runs as a service", listen, launchdLabel)
	}
	return ln.Close()
}

// writeArchive packs the Homelab folder, minus skipOnMove, into a private .tar.gz.
func writeArchive(home, dest string) (int, error) {
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(dest)
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	n := 0
	err = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(home, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		top, _, _ := strings.Cut(rel, "/")
		if skipOnMove[top] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, ".imported") || strings.HasSuffix(rel, ".tmp") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil // sockets, symlinks and the like aren't data
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		hdr.Uname, hdr.Gname, hdr.Uid, hdr.Gid = "", "", 0, 0
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		src, err := os.Open(p)
		if err != nil {
			return fmt.Errorf("can't read %s: %w", p, err)
		}
		defer src.Close()
		if _, err := io.Copy(tw, src); err != nil {
			return fmt.Errorf("copy %s: %w", p, err)
		}
		n++
		return nil
	})
	if err != nil {
		return 0, err
	}
	if err := tw.Close(); err != nil {
		return 0, err
	}
	if err := gz.Close(); err != nil {
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	ok = true
	return n, nil
}

// extractArchive unpacks a move file into home. Only folders and plain files are taken, and
// nothing may land outside home.
func extractArchive(src, home string) (int, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, fmt.Errorf("%s isn't a Homelab move file: %w", src, err)
	}
	tr := tar.NewReader(gz)
	n := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return n, fmt.Errorf("%s is damaged: %w", src, err)
		}
		name := filepath.FromSlash(strings.TrimSuffix(hdr.Name, "/"))
		if name == "" || filepath.IsAbs(name) || !filepath.IsLocal(name) {
			return n, fmt.Errorf("%s has an unsafe entry %q", src, hdr.Name)
		}
		top, _, _ := strings.Cut(filepath.ToSlash(name), "/")
		if skipOnMove[top] {
			continue
		}
		target := filepath.Join(home, name)
		mode := os.FileMode(hdr.Mode).Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, mode|0o700); err != nil {
				return n, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return n, err
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode|0o600)
			if err != nil {
				return n, err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return n, err
			}
			if err := out.Close(); err != nil {
				return n, err
			}
			_ = os.Chtimes(target, hdr.ModTime, hdr.ModTime)
			n++
		}
	}
	return n, nil
}

// removeTailscaleMachines deletes this computer's Homelab machines from the tailnet, so the
// device can take their names. Without the OAuth client it says how to do it by hand.
func removeTailscaleMachines(home string, cfg Config, out io.Writer) {
	byHand := "In the Tailscale admin console, open Machines and delete the machines this Mac ran for Homelab " +
		"(homelab and one per app, such as movies and movies-dev), so the device can use those names."
	dir := filepath.Join(home, "secrets")
	secrets, err := secretstore.Open(filepath.Join(dir, "secrets.db"), filepath.Join(dir, "master.key"))
	if err != nil {
		fmt.Fprintln(out, byHand)
		return
	}
	defer secrets.Close()
	client, err := tailscaleClient(secrets)
	if err != nil {
		fmt.Fprintln(out, byHand)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	names, err := client.RemoveHomelabMachines(ctx, cfg.Tailscale.Tags)
	if len(names) > 0 {
		fmt.Fprintf(out, "Removed this Mac's Homelab machines from the tailnet: %s.\n", strings.Join(names, ", "))
	}
	if err != nil {
		fmt.Fprintf(out, "Couldn't remove every Tailscale machine (%v). %s\n", err, byHand)
	}
}

// removeContainers stops and removes the containers Homelab ran on this computer. Their
// data stays in the Homelab folder.
func removeContainers(out io.Writer) {
	var ids []string
	for _, filter := range []string{"label=homelab.app", "label=com.docker.compose.project=homelab-pocketid"} {
		b, err := exec.Command("docker", "ps", "-aq", "--filter", filter).Output()
		if err != nil {
			fmt.Fprintln(out, "Couldn't list Homelab's containers here; stop them by hand if they still run.")
			return
		}
		ids = append(ids, strings.Fields(string(b))...)
	}
	if len(ids) == 0 {
		return
	}
	if b, err := exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).CombinedOutput(); err != nil {
		fmt.Fprintf(out, "Couldn't remove Homelab's containers here: %s\n", strings.TrimSpace(string(b)))
		return
	}
	fmt.Fprintf(out, "Stopped and removed Homelab's %d container(s) on this computer.\n", len(ids))
}

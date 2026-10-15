package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/state"
)

func cmdRecover(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("recover", flag.ContinueOnError)
	homeFlag := flags.String("home", "", "Homelab folder (default ~/.homelab)")
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
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return err
	}
	store, err := state.Open(filepath.Join(home, "homelab.db"))
	if err != nil {
		return fmt.Errorf("open Homelab state: %w", err)
	}
	defer store.Close()
	people, err := store.People(context.Background())
	if err != nil {
		return err
	}
	var owner *core.Person
	for i := range people {
		for _, role := range people[i].Roles {
			if role == "owner" {
				owner = &people[i]
				break
			}
		}
	}
	if owner == nil {
		return fmt.Errorf("there isn't a household owner to recover yet")
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	value := base64.RawURLEncoding.EncodeToString(token[:])
	digest := sha256.Sum256([]byte(value))
	expires := time.Now().Add(15 * time.Minute)
	if err := store.PutOneTimeCode(context.Background(), digest[:], owner.ID, "recovery-link", expires); err != nil {
		return err
	}
	if err := store.Log(context.Background(), state.Event{Actor: owner.Email, Action: "recovery.link.created", Detail: "Created a one-time owner sign-in link"}); err != nil {
		return err
	}
	fmt.Fprintf(out, "One-time sign-in link (expires at %s):\nhttp://localhost:%s/recover/%s\n", expires.Local().Format(time.RFC3339), port, value)
	return nil
}

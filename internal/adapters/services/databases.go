package services

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// Databases is core.Databases backed by homelab-services, called as the system caller like
// Secrets. homelab-services keeps what it is told, so it knows the databases again after a
// restart.
type Databases struct{ s *Services }

var _ core.Databases = (*Databases)(nil)

// Databases returns the databases client.
func (s *Services) Databases() *Databases { return &Databases{s: s} }

// errNoServices is what database calls fail with while homelab-services isn't running.
var errNoServices = errors.New("Homelab's services aren't running right now")

// snapshotTimeout bounds a copy of a database file, which can be big.
const snapshotTimeout = 10 * time.Minute

func (d *Databases) call(method, path string, timeout time.Duration, in any) error {
	_, err := d.s.call("databases", errNoServices, timeout, method, path, url.Values{}, in)
	return err
}

func dbPath(app, env string) string {
	return "/internal/db/" + url.PathEscape(app) + "/" + url.PathEscape(env)
}

// Register tells homelab-services where an environment's database is.
func (d *Databases) Register(app, env string, db core.DatabaseRegistration) error {
	return d.call(http.MethodPut, dbPath(app, env), callTimeout, db)
}

// Forget makes homelab-services forget every environment of an app.
func (d *Databases) Forget(app string) error {
	return d.call(http.MethodDelete, "/internal/db/"+url.PathEscape(app), callTimeout, nil)
}

// Snapshot has homelab-services write a consistent copy of a sqlite file to `to`.
func (d *Databases) Snapshot(app, env, to string) error {
	if !strings.HasPrefix(to, "/") {
		return errors.New("the copy's path must be absolute")
	}
	return d.call(http.MethodPost, dbPath(app, env)+"/snapshot", snapshotTimeout, map[string]string{"to": to})
}

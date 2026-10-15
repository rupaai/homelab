// Package state is Homelab's desired state and history, kept in one SQLite file.
// Everything the reconciler needs to rebuild the running system after a restart is here.
package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure Go, so homelabd cross-compiles without a C toolchain

	"github.com/rupaai/homelab/internal/core"
)

// App is one registered app.
type App struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Repo      string    `json:"repo"`      // e.g. rupaai/smart-recommender-player
	Manifest  []byte    `json:"-"`         // latest homelab.yaml seen on the build branch
	Promotion string    `json:"promotion"` // manual | auto-dev | auto
	Slot      int       `json:"slot"`      // picks the app's local ports; never reused
	AddedBy   string    `json:"addedBy"`
	AddedAt   time.Time `json:"addedAt"`
}

// Env is the desired state of one app environment.
type Env struct {
	App  string
	Env  string
	Want *core.Release // nil means "nothing should run"
	// LastBuild is the newest build commit already handled for auto-dev, so a build is
	// deployed once, even if someone later deploys an older commit by hand.
	LastBuild string
}

// Deployment is one entry in an environment's history: what ran, from when, set by whom.
type Deployment struct {
	ID         int64        `json:"id"`
	App        string       `json:"app"`
	Env        string       `json:"env"`
	Release    core.Release `json:"release"`
	By         string       `json:"by"`
	At         time.Time    `json:"at"`
	Kind       string       `json:"kind"`                 // deploy | promote | rollback
	BackupPath string       `json:"backupPath,omitempty"` // data copy taken before this deployment
}

// Event is one line of the activity log (O3).
type Event struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	App    string    `json:"app,omitempty"`
	Env    string    `json:"env,omitempty"`
	Action string    `json:"action"` // e.g. app.added, deploy.started, deploy.done, promote.failed
	Detail string    `json:"detail"`
}

// Store is the SQLite-backed state.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (and creates or upgrades) the state file.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; keeps SQLite simple and safe
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the state file.
func (s *Store) Close() error { return s.db.Close() }

var migrations = []string{
	`CREATE TABLE apps (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, repo TEXT NOT NULL, manifest BLOB NOT NULL,
		promotion TEXT NOT NULL, slot INTEGER NOT NULL UNIQUE, added_by TEXT NOT NULL, added_at INTEGER NOT NULL);
	CREATE TABLE envs (
		app TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE, env TEXT NOT NULL,
		want TEXT, last_build TEXT NOT NULL DEFAULT '', PRIMARY KEY (app, env));
	CREATE TABLE deployments (
		id INTEGER PRIMARY KEY AUTOINCREMENT, app TEXT NOT NULL, env TEXT NOT NULL,
		release TEXT NOT NULL, by TEXT NOT NULL, at INTEGER NOT NULL, kind TEXT NOT NULL,
		backup_path TEXT NOT NULL DEFAULT '');
	CREATE INDEX deployments_env ON deployments(app, env, id);
	CREATE TABLE events (
		id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, actor TEXT NOT NULL,
		app TEXT NOT NULL DEFAULT '', env TEXT NOT NULL DEFAULT '', action TEXT NOT NULL, detail TEXT NOT NULL);
	CREATE INDEX events_app ON events(app, id);`,
	// Milestone 2: commits auto-promote must leave alone (failed smoke test, rolled back).
	`CREATE TABLE holds (
		app TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE, commit_sha TEXT NOT NULL,
		reason TEXT NOT NULL, at INTEGER NOT NULL, PRIMARY KEY (app, commit_sha));`,
	`CREATE TABLE people (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, email TEXT NOT NULL DEFAULT '', identity_id TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL, created_at INTEGER NOT NULL, last_sign_in INTEGER);
	CREATE UNIQUE INDEX people_email ON people(email) WHERE email <> '';
	CREATE TABLE person_roles (person_id TEXT NOT NULL REFERENCES people(id) ON DELETE CASCADE, role TEXT NOT NULL, PRIMARY KEY(person_id,role));
	CREATE UNIQUE INDEX one_household_owner ON person_roles(role) WHERE role='owner';
	CREATE TABLE person_grants (person_id TEXT NOT NULL REFERENCES people(id) ON DELETE CASCADE,
		app TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE, level TEXT NOT NULL CHECK(level IN ('use','build')), PRIMARY KEY(person_id,app));
	CREATE TABLE invites (token_hash BLOB PRIMARY KEY, person_id TEXT NOT NULL REFERENCES people(id) ON DELETE CASCADE,
		expires_at INTEGER NOT NULL, used_at INTEGER, created_at INTEGER NOT NULL);
	CREATE TABLE sessions (id_hash BLOB PRIMARY KEY, person_id TEXT NOT NULL REFERENCES people(id) ON DELETE CASCADE,
		created_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL, expires_at INTEGER NOT NULL);
	CREATE INDEX sessions_person ON sessions(person_id);
	CREATE TABLE recovery (person_id TEXT PRIMARY KEY REFERENCES people(id) ON DELETE CASCADE, code_hash TEXT NOT NULL, used_at INTEGER);
	CREATE TABLE one_time_codes (code_hash BLOB PRIMARY KEY, person_id TEXT NOT NULL REFERENCES people(id) ON DELETE CASCADE,
		purpose TEXT NOT NULL, expires_at INTEGER NOT NULL, used_at INTEGER);`,
	`CREATE TABLE app_auth_codes (
		code_hash BLOB PRIMARY KEY, person_id TEXT NOT NULL REFERENCES people(id) ON DELETE CASCADE,
		app TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE, env TEXT NOT NULL,
		return_path TEXT NOT NULL, expires_at INTEGER NOT NULL);
	CREATE TABLE app_sessions (
		token_hash BLOB PRIMARY KEY, person_id TEXT NOT NULL REFERENCES people(id) ON DELETE CASCADE,
		app TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE, env TEXT NOT NULL,
		created_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL, expires_at INTEGER NOT NULL);
	CREATE INDEX app_sessions_person ON app_sessions(person_id);
	CREATE INDEX app_sessions_app ON app_sessions(app,env);`,
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (v INTEGER NOT NULL)`); err != nil {
		return err
	}
	var v int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(v), 0) FROM schema_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("state migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version (v) VALUES (?)`, i+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// releaseJSON stores a release including its manifest (which core.Release hides from JSON).
type releaseJSON struct {
	core.Release
	Manifest []byte `json:"manifest"`
}

func encodeRelease(r *core.Release) (sql.NullString, error) {
	if r == nil {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(releaseJSON{Release: *r, Manifest: r.Manifest})
	return sql.NullString{String: string(b), Valid: true}, err
}

func decodeRelease(ns sql.NullString) (*core.Release, error) {
	if !ns.Valid {
		return nil, nil
	}
	var rj releaseJSON
	if err := json.Unmarshal([]byte(ns.String), &rj); err != nil {
		return nil, err
	}
	r := rj.Release
	r.Manifest = rj.Manifest
	return &r, nil
}

// AddApp registers an app with an empty dev and prod environment.
func (s *Store) AddApp(ctx context.Context, a App) (App, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return a, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(slot), 0) + 1 FROM apps`).Scan(&a.Slot); err != nil {
		return a, err
	}
	a.AddedAt = s.now()
	_, err = tx.ExecContext(ctx, `INSERT INTO apps (id, name, repo, manifest, promotion, slot, added_by, added_at) VALUES (?,?,?,?,?,?,?,?)`,
		a.ID, a.Name, a.Repo, a.Manifest, a.Promotion, a.Slot, a.AddedBy, a.AddedAt.UnixMilli())
	if err != nil {
		return a, fmt.Errorf("add app %s: %w", a.ID, err)
	}
	for _, e := range []string{core.Dev, core.Prod} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO envs (app, env) VALUES (?, ?)`, a.ID, e); err != nil {
			return a, err
		}
	}
	return a, tx.Commit()
}

// UpdateApp stores a newer manifest and the name it declares. The promotion mode is a
// Homelab setting (SetPromotion), so a new manifest doesn't change it.
func (s *Store) UpdateApp(ctx context.Context, id, name string, manifest []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE apps SET name = ?, manifest = ? WHERE id = ?`, name, manifest, id)
	return err
}

// SetPromotion changes an app's promotion mode.
func (s *Store) SetPromotion(ctx context.Context, id, mode string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE apps SET promotion = ? WHERE id = ?`, mode, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrNotFound
	}
	return nil
}

// RemoveApp forgets an app: its environments, history and holds. The activity log keeps
// its entries.
func (s *Store) RemoveApp(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM deployments WHERE app = ?`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM apps WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrNotFound
	}
	return tx.Commit()
}

// Hold is a commit auto-promote leaves alone.
type Hold struct {
	Commit string    `json:"commit"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// AddHold records that auto-promote must not promote commit. A second hold keeps the first reason.
func (s *Store) AddHold(ctx context.Context, app, commit, reason string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO holds (app, commit_sha, reason, at) VALUES (?,?,?,?)`, app, commit, reason, s.now().UnixMilli())
	return err
}

// Holds lists an app's held commits, newest first.
func (s *Store) Holds(ctx context.Context, app string) ([]Hold, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT commit_sha, reason, at FROM holds WHERE app = ? ORDER BY at DESC LIMIT 20`, app)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hold
	for rows.Next() {
		var h Hold
		var at int64
		if err := rows.Scan(&h.Commit, &h.Reason, &at); err != nil {
			return nil, err
		}
		h.At = time.UnixMilli(at)
		out = append(out, h)
	}
	return out, rows.Err()
}

// Apps lists every app, oldest first.
func (s *Store) Apps(ctx context.Context) ([]App, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, repo, manifest, promotion, slot, added_by, added_at FROM apps ORDER BY slot`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []App
	for rows.Next() {
		var a App
		var at int64
		if err := rows.Scan(&a.ID, &a.Name, &a.Repo, &a.Manifest, &a.Promotion, &a.Slot, &a.AddedBy, &at); err != nil {
			return nil, err
		}
		a.AddedAt = time.UnixMilli(at)
		out = append(out, a)
	}
	return out, rows.Err()
}

// App returns one app, or core.ErrNotFound.
func (s *Store) App(ctx context.Context, id string) (App, error) {
	apps, err := s.Apps(ctx)
	if err != nil {
		return App{}, err
	}
	for _, a := range apps {
		if a.ID == id {
			return a, nil
		}
	}
	return App{}, core.ErrNotFound
}

// Env returns an environment's desired state.
func (s *Store) Env(ctx context.Context, app, env string) (Env, error) {
	e := Env{App: app, Env: env}
	var want sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT want, last_build FROM envs WHERE app = ? AND env = ?`, app, env).Scan(&want, &e.LastBuild)
	if errors.Is(err, sql.ErrNoRows) {
		return e, core.ErrNotFound
	}
	if err != nil {
		return e, err
	}
	e.Want, err = decodeRelease(want)
	return e, err
}

// SetWant records what should run in an environment and adds it to the history, in one step.
func (s *Store) SetWant(ctx context.Context, d Deployment) (Deployment, error) {
	enc, err := encodeRelease(&d.Release)
	if err != nil {
		return d, err
	}
	d.At = s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE envs SET want = ? WHERE app = ? AND env = ?`, enc, d.App, d.Env)
	if err != nil {
		return d, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return d, core.ErrNotFound
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO deployments (app, env, release, by, at, kind, backup_path) VALUES (?,?,?,?,?,?,?)`,
		d.App, d.Env, enc.String, d.By, d.At.UnixMilli(), d.Kind, d.BackupPath)
	if err != nil {
		return d, err
	}
	d.ID, _ = r.LastInsertId()
	return d, tx.Commit()
}

// SetLastBuild records the newest build handled for an environment.
func (s *Store) SetLastBuild(ctx context.Context, app, env, commit string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE envs SET last_build = ? WHERE app = ? AND env = ?`, commit, app, env)
	return err
}

// History lists an environment's deployments, newest first.
func (s *Store) History(ctx context.Context, app, env string, limit int) ([]Deployment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, release, by, at, kind, backup_path FROM deployments WHERE app = ? AND env = ? ORDER BY id DESC LIMIT ?`, app, env, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Deployment
	for rows.Next() {
		d := Deployment{App: app, Env: env}
		var rel string
		var at int64
		if err := rows.Scan(&d.ID, &rel, &d.By, &at, &d.Kind, &d.BackupPath); err != nil {
			return nil, err
		}
		r, err := decodeRelease(sql.NullString{String: rel, Valid: true})
		if err != nil {
			return nil, err
		}
		d.Release, d.At = *r, time.UnixMilli(at)
		out = append(out, d)
	}
	return out, rows.Err()
}

// Log adds an event to the activity log.
func (s *Store) Log(ctx context.Context, e Event) error {
	if e.At.IsZero() {
		e.At = s.now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO events (at, actor, app, env, action, detail) VALUES (?,?,?,?,?,?)`,
		e.At.UnixMilli(), e.Actor, e.App, e.Env, e.Action, e.Detail)
	return err
}

// Events lists activity, newest first. app "" means every app. before 0 means from the newest.
func (s *Store) Events(ctx context.Context, app string, before int64, limit int) ([]Event, error) {
	q := `SELECT id, at, actor, app, env, action, detail FROM events WHERE (? = '' OR app = ?) AND (? = 0 OR id < ?) ORDER BY id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, app, app, before, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.App, &e.Env, &e.Action, &e.Detail); err != nil {
			return nil, err
		}
		e.At = time.UnixMilli(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneEvents deletes activity older than keep (O3 keeps a year).
func (s *Store) PruneEvents(ctx context.Context, keep time.Duration) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE at < ?`, s.now().Add(-keep).UnixMilli())
	return err
}

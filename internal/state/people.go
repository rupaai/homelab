package state

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// OwnerExists reports whether first-run setup has finished.
func (s *Store) OwnerExists(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM person_roles WHERE role='owner'`).Scan(&n)
	return n > 0, err
}

// SavePerson creates or updates a person and replaces their roles and grants.
func (s *Store) SavePerson(ctx context.Context, p core.Person) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO people(id,name,email,identity_id,status,created_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,email=excluded.email,identity_id=excluded.identity_id,status=excluded.status`,
		p.ID, p.Name, p.Email, p.IdentityID, p.Status, s.now().UnixMilli())
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM person_roles WHERE person_id=?`, p.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM person_grants WHERE person_id=?`, p.ID); err != nil {
		return err
	}
	for _, role := range p.Roles {
		if _, err = tx.ExecContext(ctx, `INSERT INTO person_roles(person_id,role) VALUES(?,?)`, p.ID, role); err != nil {
			return err
		}
	}
	for app, level := range p.Grants {
		if _, err = tx.ExecContext(ctx, `INSERT INTO person_grants(person_id,app,level) VALUES(?,?,?)`, p.ID, app, level); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Person reads a person and their access sets.
func (s *Store) Person(ctx context.Context, id string) (core.Person, error) {
	var p core.Person
	var last sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id,name,email,identity_id,status,last_sign_in
		FROM people WHERE id=?`, id).Scan(&p.ID, &p.Name, &p.Email, &p.IdentityID, &p.Status, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return p, core.ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if last.Valid {
		v := time.UnixMilli(last.Int64)
		p.LastSignIn = &v
	}
	rows, err := s.db.QueryContext(ctx, `SELECT role FROM person_roles WHERE person_id=? ORDER BY role`, id)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			rows.Close()
			return p, err
		}
		p.Roles = append(p.Roles, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT app,level FROM person_grants WHERE person_id=? ORDER BY app`, id)
	if err != nil {
		return p, err
	}
	p.Grants = map[string]string{}
	for rows.Next() {
		var app, level string
		if err = rows.Scan(&app, &level); err != nil {
			rows.Close()
			return p, err
		}
		p.Grants[app] = level
	}
	err = rows.Err()
	rows.Close()
	return p, err
}

// PersonByEmail finds a household account without exposing its credentials.
func (s *Store) PersonByEmail(ctx context.Context, email string) (core.Person, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM people WHERE email=? COLLATE NOCASE`, email).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Person{}, core.ErrNotFound
	}
	if err != nil {
		return core.Person{}, err
	}
	return s.Person(ctx, id)
}

// PersonByIdentityID finds the person bound to a provider subject.
func (s *Store) PersonByIdentityID(ctx context.Context, identityID string) (core.Person, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM people WHERE identity_id=? AND identity_id<>''`, identityID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Person{}, core.ErrNotFound
	}
	if err != nil {
		return core.Person{}, err
	}
	return s.Person(ctx, id)
}

// RecordSignIn stores the provider subject and first successful sign-in time.
func (s *Store) RecordSignIn(ctx context.Context, id, identityID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE people SET identity_id=?,status='signed_in',last_sign_in=? WHERE id=?`, identityID, s.now().UnixMilli(), id)
	return err
}

// RecordIdentityID stores the provider's canonical user id without marking the person signed in.
func (s *Store) RecordIdentityID(ctx context.Context, id, identityID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE people SET identity_id=? WHERE id=?`, identityID, id)
	return err
}

// People lists household accounts.
func (s *Store) People(ctx context.Context) ([]core.Person, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM people ORDER BY name COLLATE NOCASE,id`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var out []core.Person
	for _, id := range ids {
		p, err := s.Person(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// DeletePerson removes a person and all child records.
func (s *Store) DeletePerson(ctx context.Context, id string) error {
	r, err := s.db.ExecContext(ctx, `DELETE FROM people WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return core.ErrNotFound
	}
	return nil
}

// StoreInvite saves an already hashed invitation that can be redeemed until
// its expiry or the invited person signs in.
func (s *Store) StoreInvite(ctx context.Context, hash []byte, personID string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO invites(token_hash,person_id,expires_at,created_at) VALUES(?,?,?,?)`, hash, personID, expires.UnixMilli(), s.now().UnixMilli())
	return err
}

// RevokeInvites removes every invite for a person.
func (s *Store) RevokeInvites(ctx context.Context, personID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM invites WHERE person_id=?`, personID)
	return err
}

// ConsumeInvite returns the person for an unexpired invite. The invite remains
// redeemable until sign-in so a fresh one-time identity-provider link can be
// issued if the previous one expired or was opened in the wrong flow.
func (s *Store) ConsumeInvite(ctx context.Context, hash []byte) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT person_id FROM invites WHERE token_hash=? AND expires_at>?`, hash, s.now().UnixMilli()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", core.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// PutSession stores a digest, never its browser token.
func (s *Store) PutSession(ctx context.Context, hash []byte, personID string, created, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(id_hash,person_id,created_at,last_seen_at,expires_at) VALUES(?,?,?,?,?)`, hash, personID, created.UnixMilli(), created.UnixMilli(), expires.UnixMilli())
	return err
}

// SessionPerson validates session deadlines and refreshes the idle timeout.
func (s *Store) SessionPerson(ctx context.Context, hash []byte, now time.Time, idle time.Duration) (core.Person, error) {
	var id string
	var last, expires int64
	err := s.db.QueryRowContext(ctx, `SELECT person_id,last_seen_at,expires_at FROM sessions WHERE id_hash=?`, hash).Scan(&id, &last, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Person{}, core.ErrNotFound
	}
	if err != nil {
		return core.Person{}, err
	}
	if now.UnixMilli()-last > idle.Milliseconds() || now.UnixMilli() > expires {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash=?`, hash)
		return core.Person{}, core.ErrNotFound
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at=? WHERE id_hash=?`, now.UnixMilli(), hash); err != nil {
		return core.Person{}, err
	}
	return s.Person(ctx, id)
}

// DeleteSessions ends every Homelab session for a person.
func (s *Store) DeleteSessions(ctx context.Context, personID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE person_id=?`, personID)
	return err
}

// DeleteAppSessions ends every app-bound browser session for a person.
func (s *Store) DeleteAppSessions(ctx context.Context, personID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_sessions WHERE person_id=?`, personID)
	return err
}

// DeleteSession ends one Homelab session.
func (s *Store) DeleteSession(ctx context.Context, hash []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash=?`, hash)
	return err
}

// SetRecovery stores the one-way recovery-code hash.
func (s *Store) SetRecovery(ctx context.Context, personID, hash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO recovery(person_id,code_hash) VALUES(?,?) ON CONFLICT(person_id) DO UPDATE SET code_hash=excluded.code_hash,used_at=NULL`, personID, hash)
	return err
}

// RecoveryHash returns the recovery hash and whether it has been used.
func (s *Store) RecoveryHash(ctx context.Context, personID string) (string, bool, error) {
	var hash string
	var used sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT code_hash,used_at FROM recovery WHERE person_id=?`, personID).Scan(&hash, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, core.ErrNotFound
	}
	return hash, used.Valid, err
}

// UseRecovery marks a recovery code consumed.
func (s *Store) UseRecovery(ctx context.Context, personID string) error {
	r, err := s.db.ExecContext(ctx, `UPDATE recovery SET used_at=? WHERE person_id=? AND used_at IS NULL`, s.now().UnixMilli(), personID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return core.ErrNotFound
	}
	return nil
}

// PutOneTimeCode stores a one-time code digest for recovery or a proxy exchange.
func (s *Store) PutOneTimeCode(ctx context.Context, hash []byte, personID, purpose string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO one_time_codes(code_hash,person_id,purpose,expires_at) VALUES(?,?,?,?)`, hash, personID, purpose, expires.UnixMilli())
	return err
}

// ConsumeOneTimeCode validates and consumes a code exactly once.
func (s *Store) ConsumeOneTimeCode(ctx context.Context, hash []byte, purpose string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT person_id FROM one_time_codes WHERE code_hash=? AND purpose=? AND used_at IS NULL AND expires_at>?`, hash, purpose, s.now().UnixMilli()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", core.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE one_time_codes SET used_at=? WHERE code_hash=? AND used_at IS NULL`, s.now().UnixMilli(), hash); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

// PutAppCode stores a short-lived code bound to one app session and return path.
func (s *Store) PutAppCode(ctx context.Context, hash []byte, personID, app, env, returnPath string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_auth_codes(code_hash,person_id,app,env,return_path,expires_at) VALUES(?,?,?,?,?,?)`, hash, personID, app, env, returnPath, expires.UnixMilli())
	return err
}

// AppCode is the consumed app authorization code's payload.
type AppCode struct {
	PersonID   string
	App        string
	Env        string
	ReturnPath string
}

// ConsumeAppCode removes a valid code, so only one callback can exchange it.
func (s *Store) ConsumeAppCode(ctx context.Context, hash []byte, now time.Time) (AppCode, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AppCode{}, err
	}
	defer tx.Rollback()
	var c AppCode
	var expires int64
	err = tx.QueryRowContext(ctx, `SELECT person_id,app,env,return_path,expires_at FROM app_auth_codes WHERE code_hash=?`, hash).Scan(&c.PersonID, &c.App, &c.Env, &c.ReturnPath, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return AppCode{}, core.ErrNotFound
	}
	if err != nil {
		return AppCode{}, err
	}
	if now.UnixMilli() >= expires {
		return AppCode{}, core.ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM app_auth_codes WHERE code_hash=?`, hash); err != nil {
		return AppCode{}, err
	}
	return c, tx.Commit()
}

// PutAppSession stores only a digest of the browser's short-lived app cookie.
func (s *Store) PutAppSession(ctx context.Context, hash []byte, personID, app, env string, created, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_sessions(token_hash,person_id,app,env,created_at,last_seen_at,expires_at) VALUES(?,?,?,?,?,?,?)`, hash, personID, app, env, created.UnixMilli(), created.UnixMilli(), expires.UnixMilli())
	return err
}

// AppSessionPerson validates one app's cookie and refreshes its idle timeout.
func (s *Store) AppSessionPerson(ctx context.Context, hash []byte, app, env string, now time.Time, idle time.Duration) (core.Person, error) {
	var id string
	var last, expires int64
	err := s.db.QueryRowContext(ctx, `SELECT person_id,last_seen_at,expires_at FROM app_sessions WHERE token_hash=? AND app=? AND env=?`, hash, app, env).Scan(&id, &last, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Person{}, core.ErrNotFound
	}
	if err != nil {
		return core.Person{}, err
	}
	if now.UnixMilli()-last > idle.Milliseconds() || now.UnixMilli() > expires {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM app_sessions WHERE token_hash=?`, hash)
		return core.Person{}, core.ErrNotFound
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE app_sessions SET last_seen_at=? WHERE token_hash=?`, now.UnixMilli(), hash); err != nil {
		return core.Person{}, err
	}
	return s.Person(ctx, id)
}

// ActivateWithPassword signs an invited person in for the first time with their own password.
// It fails with ErrNotFound unless they are still invited, and ends their invites, all in one step.
func (s *Store) ActivateWithPassword(ctx context.Context, id, identityID, hash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `UPDATE people SET identity_id=?,status='signed_in',last_sign_in=? WHERE id=? AND status='invited'`, identityID, s.now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return core.ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO person_passwords(person_id,hash) VALUES(?,?) ON CONFLICT(person_id) DO UPDATE SET hash=excluded.hash`, id, hash); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM invites WHERE person_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// PasswordHash returns a person's own password hash, or ErrNotFound when they have none.
func (s *Store) PasswordHash(ctx context.Context, personID string) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT hash FROM person_passwords WHERE person_id=?`, personID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", core.ErrNotFound
	}
	return hash, err
}

// DeletePassword forgets a person's own password. Having none is not an error.
func (s *Store) DeletePassword(ctx context.Context, personID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM person_passwords WHERE person_id=?`, personID)
	return err
}

// AnyPassword says whether any person has chosen their own password.
func (s *Store) AnyPassword(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM person_passwords`).Scan(&n)
	return n > 0, err
}

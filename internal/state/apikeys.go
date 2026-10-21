package state

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// APIKey is one stored API key, without the key itself (only its hash is kept).
type APIKey struct {
	ID        string     `json:"id"`
	Hash      []byte     `json:"-"`
	Prefix    string     `json:"prefix"` // the first characters of the key, so people can tell keys apart
	Name      string     `json:"name"`
	Kind      string     `json:"kind"` // app | admin
	App       string     `json:"app,omitempty"`
	Env       string     `json:"env,omitempty"`
	Scopes    []string   `json:"scopes"`
	CreatedBy string     `json:"createdBy,omitempty"` // person id, admin keys only
	CreatedAt time.Time  `json:"createdAt"`
	LastUsed  *time.Time `json:"lastUsed,omitempty"`
	Expires   *time.Time `json:"expires,omitempty"`
	Revoked   *time.Time `json:"revoked,omitempty"`
}

const apiKeyCols = `id,hash,prefix,name,kind,COALESCE(app,''),env,scopes,COALESCE(created_by,''),created_at,last_used_at,expires_at,revoked_at`

func millis(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

func fromMillis(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.UnixMilli(n.Int64)
	return &t
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

type scanner interface{ Scan(...any) error }

func scanAPIKey(r scanner) (APIKey, error) {
	var k APIKey
	var scopes string
	var created int64
	var last, expires, revoked sql.NullInt64
	if err := r.Scan(&k.ID, &k.Hash, &k.Prefix, &k.Name, &k.Kind, &k.App, &k.Env, &scopes, &k.CreatedBy, &created, &last, &expires, &revoked); err != nil {
		return k, err
	}
	k.Scopes = []string{}
	if scopes != "" {
		k.Scopes = strings.Split(scopes, " ")
	}
	k.CreatedAt = time.UnixMilli(created)
	k.LastUsed, k.Expires, k.Revoked = fromMillis(last), fromMillis(expires), fromMillis(revoked)
	return k, nil
}

// PutAPIKey stores a new key. For an app key, any earlier key of that app environment is
// revoked in the same transaction, so there is never more than one.
func (s *Store) PutAPIKey(ctx context.Context, k APIKey) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	if k.Kind == "app" {
		if _, err := tx.ExecContext(ctx, `UPDATE api_keys SET revoked_at=? WHERE kind='app' AND app=? AND env=? AND revoked_at IS NULL`, now, k.App, k.Env); err != nil {
			return err
		}
	}
	if k.CreatedAt.IsZero() {
		k.CreatedAt = s.now()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO api_keys(id,hash,prefix,name,kind,app,env,scopes,created_by,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		k.ID, k.Hash, k.Prefix, k.Name, k.Kind, nullString(k.App), k.Env, strings.Join(k.Scopes, " "), nullString(k.CreatedBy), k.CreatedAt.UnixMilli(), millis(k.Expires))
	if err != nil {
		return err
	}
	return tx.Commit()
}

// SetAPIKeyScopes changes what a key that is not revoked may do. Its token stays the same.
func (s *Store) SetAPIKeyScopes(ctx context.Context, id string, scopes []string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE api_keys SET scopes=? WHERE id=? AND revoked_at IS NULL`, strings.Join(scopes, " "), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrNotFound
	}
	return nil
}

// APIKeyByHash finds a key by the SHA-256 of its value, revoked or not.
func (s *Store) APIKeyByHash(ctx context.Context, hash []byte) (APIKey, error) {
	k, err := scanAPIKey(s.db.QueryRowContext(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE hash=?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return k, core.ErrNotFound
	}
	return k, err
}

// APIKeyByID finds a key by its id.
func (s *Store) APIKeyByID(ctx context.Context, id string) (APIKey, error) {
	k, err := scanAPIKey(s.db.QueryRowContext(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return k, core.ErrNotFound
	}
	return k, err
}

// APIKeys lists the keys of one kind that aren't revoked, newest first.
func (s *Store) APIKeys(ctx context.Context, kind string) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE kind=? AND revoked_at IS NULL ORDER BY created_at DESC, id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeAPIKey stops a key working. Revoking one that is already revoked is not an error;
// an unknown id is core.ErrNotFound.
func (s *Store) RevokeAPIKey(ctx context.Context, id string) error {
	r, err := s.db.ExecContext(ctx, `UPDATE api_keys SET revoked_at=COALESCE(revoked_at,?) WHERE id=?`, s.now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return core.ErrNotFound
	}
	return nil
}

// TouchAPIKey records when a key was last used.
func (s *Store) TouchAPIKey(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at=? WHERE id=?`, at.UnixMilli(), id)
	return err
}

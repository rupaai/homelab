package state

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// PushDevice is one person's device that receives notifications.
type PushDevice struct {
	PersonID string
	Device   string // a label people recognise, e.g. "iPhone"
	Sub      core.PushSubscription
}

// SavePushSubscription stores or moves a device's subscription to personID.
func (s *Store) SavePushSubscription(ctx context.Context, personID, device string, sub core.PushSubscription) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO push_subscriptions(endpoint,person_id,p256dh,auth,device,created_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(endpoint) DO UPDATE SET person_id=excluded.person_id,p256dh=excluded.p256dh,auth=excluded.auth,device=excluded.device`,
		sub.Endpoint, personID, sub.P256dh, sub.Auth, device, s.now().UnixMilli())
	return err
}

// DeletePushSubscription forgets a device. personID "" deletes it whoever it belongs to.
func (s *Store) DeletePushSubscription(ctx context.Context, personID, endpoint string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE endpoint=? AND (?='' OR person_id=?)`, endpoint, personID, personID)
	return err
}

// PushDevices lists the subscribed devices of the given people (all people when ids is nil).
func (s *Store) PushDevices(ctx context.Context, ids []string) ([]PushDevice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT person_id,device,endpoint,p256dh,auth FROM push_subscriptions ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []PushDevice
	for rows.Next() {
		var d PushDevice
		if err := rows.Scan(&d.PersonID, &d.Device, &d.Sub.Endpoint, &d.Sub.P256dh, &d.Sub.Auth); err != nil {
			return nil, err
		}
		if ids == nil || want[d.PersonID] {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

// OpenAlert records that the problem key started at since, if it isn't open already.
// It returns when the open incident started and whether a notification was sent for it.
func (s *Store) OpenAlert(ctx context.Context, key string, since time.Time) (time.Time, bool, error) {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO alerts(key,since) VALUES(?,?) ON CONFLICT(key) DO NOTHING`, key, since.UnixMilli()); err != nil {
		return time.Time{}, false, err
	}
	var at int64
	var sent sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT since,sent_at FROM alerts WHERE key=?`, key).Scan(&at, &sent); err != nil {
		return time.Time{}, false, err
	}
	return time.UnixMilli(at), sent.Valid, nil
}

// MarkAlertSent records that people were told about the open incident key.
func (s *Store) MarkAlertSent(ctx context.Context, key string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alerts SET sent_at=? WHERE key=?`, at.UnixMilli(), key)
	return err
}

// CloseAlert ends the incident key and says whether people had been told about it.
func (s *Store) CloseAlert(ctx context.Context, key string) (bool, error) {
	var sent sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT sent_at FROM alerts WHERE key=?`, key).Scan(&sent)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM alerts WHERE key=?`, key)
	return sent.Valid, err
}

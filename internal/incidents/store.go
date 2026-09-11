// Package incidents persists observations, evidence, and notification intent.
package incidents

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ibrahimmuh26/monitoring-container/internal/agent"
	"github.com/ibrahimmuh26/monitoring-container/internal/config"
	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	lock *os.File
}
type Incident struct {
	ID          string             `json:"id"`
	Reason      string             `json:"reason"`
	Observation agent.Observation  `json:"observation"`
	LogStatus   string             `json:"log_status"`
	Logs        string             `json:"logs"`
	Closed      bool               `json:"closed"`
	Clearance   *agent.Observation `json:"clearance,omitempty"`
}
type Event struct {
	ID                       int64
	IncidentID, Kind, ChatID string
	Attempts                 int
}

func Open(dir string, maxBytes int64) (_ *Store, err error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) == "/" || maxBytes < 8<<20 || maxBytes > 1<<30 {
		return nil, errors.New("invalid incident storage bounds")
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "agent.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another agent owns this data directory")
	}
	s := &Store{lock: lock}
	defer func() {
		if err != nil {
			s.Close()
		}
	}()
	file := filepath.Join(dir, "incidents.sqlite")
	f, err := os.OpenFile(file, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	s.db, err = sql.Open("sqlite", file)
	if err != nil {
		return nil, err
	}
	s.db.SetMaxOpenConns(1)
	for _, statement := range []string{
		"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=DELETE", "PRAGMA auto_vacuum=FULL", "PRAGMA temp_store=MEMORY", "PRAGMA foreign_keys=ON",
		fmt.Sprintf("PRAGMA max_page_count=%d", maxBytes/4096),
		`CREATE TABLE IF NOT EXISTS delivery_control (key TEXT PRIMARY KEY, deadline INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS states (target TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, failures INTEGER NOT NULL, successes INTEGER NOT NULL, active TEXT NOT NULL, checked INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS incidents (id TEXT PRIMARY KEY, target TEXT NOT NULL, reason TEXT NOT NULL, observation TEXT NOT NULL, opened INTEGER NOT NULL, closed INTEGER NOT NULL DEFAULT 0, log_status TEXT NOT NULL DEFAULT 'pending', logs TEXT NOT NULL DEFAULT '', chat_id TEXT NOT NULL, resolution TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS outbox (id INTEGER PRIMARY KEY AUTOINCREMENT, incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE, kind TEXT NOT NULL, chat_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0, next_at INTEGER NOT NULL DEFAULT 0, UNIQUE(incident_id,kind))`,
	} {
		if _, err = s.db.Exec(statement); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func (s *Store) Close() error {
	var err error
	if s.db != nil {
		err = s.db.Close()
	}
	if s.lock != nil {
		unix.Flock(int(s.lock.Fd()), unix.LOCK_UN)
		s.lock.Close()
	}
	return err
}

// Record commits threshold counters and transitions atomically. Empty reason
// with clear=false is indeterminate (e.g. startup), not proof of recovery.
func (s *Store) Record(ctx context.Context, o agent.Observation, reason string, clear bool, c config.Incidents, chatID string, maxGap time.Duration) (string, error) {
	key := o.Server + "/" + o.Target + "/" + o.Container.Name
	now := o.Time.Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var fingerprint, active string
	var failures, successes int
	var checked int64
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,failures,successes,active,checked FROM states WHERE target=?`, key).Scan(&fingerprint, &failures, &successes, &active, &checked)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	next := o.Container.ID + "/" + reason
	if fingerprint != next || now-checked > int64(maxGap.Seconds()) || now < checked {
		failures = 0
		successes = 0
	}
	opened := ""
	if reason != "" {
		failures = min(failures+1, c.FailureThreshold)
		successes = 0
		if active == "" && failures >= c.FailureThreshold {
			opened = "INC-" + rand.Text()
			active = opened
			data, err := json.Marshal(o)
			if err != nil {
				return "", err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO incidents(id,target,reason,observation,opened,chat_id) VALUES(?,?,?,?,?,?)`, opened, key, reason, string(data), now, chatID); err != nil {
				return "", err
			}
		}
	} else if clear {
		failures = 0
		successes = min(successes+1, c.SuccessThreshold)
		if active != "" && successes >= c.SuccessThreshold {
			resolution, marshalErr := json.Marshal(o)
			if marshalErr != nil {
				return "", marshalErr
			}
			if _, err = tx.ExecContext(ctx, `UPDATE incidents SET closed=?,resolution=? WHERE id=?`, now, string(resolution), active); err != nil {
				return "", err
			}
			// Resolution is queued only after evidence/open event finalization, preserving order.
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO outbox(incident_id,kind,chat_id,status) SELECT id,'condition_cleared',chat_id,CASE WHEN chat_id='' THEN 'disabled' ELSE 'pending' END FROM incidents WHERE id=? AND log_status!='pending'`, active); err != nil {
				return "", err
			}
			active = ""
		}
	} else {
		failures = 0
		successes = 0
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO states(target,fingerprint,failures,successes,active,checked) VALUES(?,?,?,?,?,?) ON CONFLICT(target) DO UPDATE SET fingerprint=excluded.fingerprint,failures=excluded.failures,successes=excluded.successes,active=excluded.active,checked=excluded.checked`, key, next, failures, successes, active, now)
	if err != nil {
		return "", err
	}
	return opened, tx.Commit()
}

func (s *Store) Get(ctx context.Context, id string) (Incident, error) {
	var i Incident
	var raw, resolution string
	var closed int64
	err := s.db.QueryRowContext(ctx, `SELECT id,reason,observation,log_status,logs,closed,resolution FROM incidents WHERE id=?`, id).Scan(&i.ID, &i.Reason, &raw, &i.LogStatus, &i.Logs, &closed, &resolution)
	if err != nil {
		return i, err
	}
	i.Closed = closed != 0
	err = json.Unmarshal([]byte(raw), &i.Observation)
	if err == nil && resolution != "" {
		err = json.Unmarshal([]byte(resolution), &i.Clearance)
	}
	return i, err
}

func (s *Store) PendingEvidence(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM incidents WHERE log_status='pending' ORDER BY opened LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// FinishEvidence only receives already sanitized log text. Raw logs never enter SQLite.
func (s *Store) FinishEvidence(ctx context.Context, id, status, logs string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE incidents SET log_status=?,logs=? WHERE id=? AND log_status='pending'`, status, logs, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO outbox(incident_id,kind,chat_id,status) SELECT id,'opened',chat_id,CASE WHEN chat_id='' THEN 'disabled' ELSE 'pending' END FROM incidents WHERE id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO outbox(incident_id,kind,chat_id,status) SELECT id,'condition_cleared',chat_id,CASE WHEN chat_id='' THEN 'disabled' ELSE 'pending' END FROM incidents WHERE id=? AND closed!=0`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) NextEvent(ctx context.Context, now time.Time) (Event, error) {
	var e Event
	// Do not send resolution before the opening event is successfully delivered.
	err := s.db.QueryRowContext(ctx, `SELECT e.id,e.incident_id,e.kind,e.chat_id,e.attempts FROM outbox e WHERE e.status='pending' AND e.next_at<=? AND COALESCE((SELECT deadline FROM delivery_control WHERE key='telegram_pause'),0)<=? AND NOT EXISTS (SELECT 1 FROM outbox p WHERE p.incident_id=e.incident_id AND p.id<e.id AND p.status='pending') ORDER BY e.id LIMIT 1`, now.Unix(), now.Unix()).Scan(&e.ID, &e.IncidentID, &e.Kind, &e.ChatID, &e.Attempts)
	return e, err
}
func (s *Store) Delivered(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE outbox SET status='sent' WHERE id=?`, id)
	return err
}
func (s *Store) Retry(ctx context.Context, id int64, next, pauseUntil time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE outbox SET attempts=attempts+1,next_at=? WHERE id=?`, next.Unix(), id); err != nil {
		return err
	}
	if !pauseUntil.IsZero() {
		if _, err = tx.ExecContext(ctx, `INSERT INTO delivery_control(key,deadline) VALUES('telegram_pause',?) ON CONFLICT(key) DO UPDATE SET deadline=MAX(delivery_control.deadline,excluded.deadline)`, pauseUntil.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) Prune(ctx context.Context, now time.Time, retention time.Duration) error {
	// Resolved incidents (including undelivered events) expire at retention. Active
	// incidents are kept; a full database fails closed instead of dropping evidence.
	cutoff := now.Add(-retention).Unix()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM incidents WHERE closed!=0 AND closed<?`, cutoff); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM states WHERE active='' AND checked<?`, cutoff)
	return err
}

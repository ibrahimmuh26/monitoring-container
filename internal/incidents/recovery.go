package incidents

import (
	"context"
	"database/sql"
	"time"

	"github.com/ibrahimmuh26/monitoring-container/internal/config"
)

// ReserveRecovery makes an action durable before it reaches Docker. This fails
// closed after a crash: an ambiguous reservation is reported, not repeated.
func (s *Store) ReserveRecovery(ctx context.Context, incidentID, target string, policy config.Recovery, now time.Time) (bool, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, "", err
	}
	defer tx.Rollback()
	var existing int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recoveries WHERE incident_id=?`, incidentID).Scan(&existing)
	if err != nil {
		return false, "", err
	}
	if existing != 0 {
		return false, "already_reserved", nil
	}
	var latest sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT MAX(requested) FROM recoveries WHERE target=?`, target).Scan(&latest); err != nil {
		return false, "", err
	}
	if latest.Valid && now.Unix()-latest.Int64 < int64(policy.Cooldown.Seconds()) {
		return false, "cooldown", nil
	}
	var hourly int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recoveries WHERE target=? AND requested>=?`, target, now.Add(-time.Hour).Unix()).Scan(&hourly); err != nil {
		return false, "", err
	}
	if hourly >= policy.MaxAttemptsPerHour {
		return false, "hourly_limit", nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO recoveries(incident_id,target,requested,status) VALUES(?,?,?,'reserved')`, incidentID, target, now.Unix()); err != nil {
		return false, "", err
	}
	if err = tx.Commit(); err != nil {
		return false, "", err
	}
	return true, "reserved", nil
}

func (s *Store) MarkReservedRecoveriesInterrupted(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE recoveries SET status='interrupted' WHERE status='reserved'`)
	return err
}

func (s *Store) FinishRecovery(ctx context.Context, incidentID, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE recoveries SET status=? WHERE incident_id=? AND status='reserved'`, status, incidentID)
	return err
}

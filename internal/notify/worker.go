package notify

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ibrahimmuh26/monitoring-container/internal/incidents"
)

type Worker struct {
	Store  *incidents.Store
	Sender Sender
	Warn   func(string)
}

// Tick sends at most one event. Delivery is at-least-once: a crash after remote
// acceptance but before the local acknowledgement can duplicate a message.
func (w *Worker) Tick(ctx context.Context, now time.Time) (time.Duration, error) {
	const pace = 4 * time.Second
	event, err := w.Store.NextEvent(ctx, now)
	if errors.Is(err, sql.ErrNoRows) {
		return pace, nil
	}
	if err != nil {
		return pace, err
	}
	incident, err := w.Store.Get(ctx, event.IncidentID)
	// Retention can remove a resolved incident between selection and loading.
	if errors.Is(err, sql.ErrNoRows) {
		return pace, nil
	}
	if err != nil {
		return pace, err
	}
	retryAfter, err := w.Sender.Send(ctx, event, incident)
	if err == nil {
		return pace, w.Store.Delivered(ctx, event.ID)
	}
	if ctx.Err() != nil {
		return pace, ctx.Err()
	}
	backoff := min(time.Duration(1<<min(event.Attempts, 9))*10*time.Second, time.Hour)
	if retryAfter > backoff {
		backoff = retryAfter
	}
	var pauseUntil time.Time
	if retryAfter > 0 {
		pauseUntil = now.Add(retryAfter)
	}
	if err = w.Store.Retry(ctx, event.ID, now.Add(backoff), pauseUntil); err != nil {
		return pace, err
	}
	if w.Warn != nil {
		w.Warn("Telegram delivery deferred; event remains queued")
	}
	// Telegram flood control pauses all deliveries, not just the failed event.
	return max(pace, retryAfter), nil
}

func (w *Worker) Run(ctx context.Context) error {
	for {
		delay, err := w.Tick(ctx, time.Now().UTC())
		if err != nil {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

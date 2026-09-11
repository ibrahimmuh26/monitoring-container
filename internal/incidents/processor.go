package incidents

import (
	"context"
	"errors"
	"time"

	"github.com/ibrahimmuh26/monitoring-container/internal/agent"
	"github.com/ibrahimmuh26/monitoring-container/internal/config"
	"github.com/ibrahimmuh26/monitoring-container/internal/diagnostics"
	"github.com/ibrahimmuh26/monitoring-container/internal/docker"
)

type Processor struct {
	Store    *Store
	Config   config.Config
	Logs     docker.LogReader
	Redactor *diagnostics.Redactor
	Restart  docker.Restarter
	Opened   func(id, reason string)
}

// Resume finalizes evidence interrupted by a previous process without pretending
// that logs collected later describe the original failure window.
func (p *Processor) Resume(ctx context.Context) error {
	if err := p.Store.MarkReservedRecoveriesInterrupted(ctx); err != nil {
		return err
	}
	for {
		ids, err := p.Store.PendingEvidence(ctx)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			if err = p.Store.FinishEvidence(ctx, id, "interrupted", ""); err != nil {
				return err
			}
		}
	}
}

func (p *Processor) Handle(ctx context.Context, o agent.Observation) error {
	reason, clear := classify(o, p.Config.Incidents.StartupGrace)
	chatID := ""
	if p.Config.Telegram.Enabled {
		chatID = p.Config.Telegram.ChatID
	}
	gap := 2 * (p.Config.Interval + time.Duration(len(p.Config.Targets))*p.Config.Docker.Timeout + p.Config.Diagnostics.Timeout)
	id, err := p.Store.Record(ctx, o, reason, clear, p.Config.Incidents, chatID, gap)
	if err != nil {
		return err
	}
	if id != "" {
		status, text := "disabled", ""
		if p.Config.Diagnostics.CollectLogs && o.Container.ID != "" {
			check, cancel := context.WithTimeout(ctx, p.Config.Diagnostics.Timeout)
			raw, truncated, collectErr := p.Logs.Logs(check, o.Container, o.Time.Add(-p.Config.Diagnostics.LogWindow), p.Config.Diagnostics.MaxLines, p.Config.Diagnostics.MaxBytes)
			cancel()
			if collectErr != nil {
				status = "unavailable"
			} else {
				status = "collected"
				if truncated {
					status = "truncated"
				}
				text = p.Redactor.Clean(raw, p.Config.Diagnostics.MaxBytes)
			}
		} else if p.Config.Diagnostics.CollectLogs {
			status = "no_container"
		}
		if err = p.recover(ctx, id, o, reason); err != nil {
			return err
		}
		if err = p.Store.FinishEvidence(ctx, id, status, text); err != nil {
			return err
		}
		if p.Opened != nil {
			p.Opened(id, reason)
		}
	}
	return p.Store.Prune(ctx, o.Time, p.Config.Incidents.Retention)
}

// recover has an intentionally narrow eligibility rule. A running unhealthy
// service may have a shared dependency failure, so it is reported but untouched.
func (p *Processor) recover(ctx context.Context, id string, o agent.Observation, reason string) error {
	if reason != "container_exited" && reason != "container_dead" {
		return nil
	}
	var target *config.Target
	for i := range p.Config.Targets {
		if p.Config.Targets[i].Name == o.Target {
			target = &p.Config.Targets[i]
			break
		}
	}
	if target == nil || !target.Recovery.Enabled || p.Restart == nil {
		return nil
	}
	allowed, _, err := p.Store.ReserveRecovery(ctx, id, o.Target, target.Recovery, o.Time)
	if err != nil || !allowed {
		return err
	}
	check, cancel := context.WithTimeout(ctx, target.Recovery.Timeout)
	err = p.Restart.Restart(check, o.Container)
	cancel()
	status := "failed"
	switch {
	case err == nil:
		status = "requested"
	case errors.Is(err, docker.ErrRestartNotNeeded):
		status = "not_needed"
	case errors.Is(err, docker.ErrNotFound):
		status = "not_found"
	case errors.Is(err, docker.ErrUnsupported):
		status = "unsupported"
	}
	return p.Store.FinishRecovery(ctx, id, status)
}

func classify(o agent.Observation, grace time.Duration) (string, bool) {
	switch o.Status {
	case "not_found":
		return "target_missing", false
	case "unavailable":
		return "inspection_unavailable", false
	case "unsupported":
		return "unsupported_target", false
	case "observed":
	default:
		return "", false
	}
	if o.Container.State != "running" {
		return "container_" + o.Container.State, false
	}
	if started, err := time.Parse(time.RFC3339Nano, o.Container.StartedAt); err == nil && o.Time.Sub(started) < grace {
		return "", false
	}
	switch o.Container.DockerHealth {
	case "starting":
		return "", false
	case "unhealthy":
		return "docker_unhealthy", false
	case "healthy", "unknown":
		return "", true
	default:
		return "", false
	}
}

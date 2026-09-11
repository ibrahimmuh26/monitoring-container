package incidents

import (
	"context"
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
	Opened   func(id, reason string)
}

// Resume finalizes evidence interrupted by a previous process without pretending
// that logs collected later describe the original failure window.
func (p *Processor) Resume(ctx context.Context) error {
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
		if err = p.Store.FinishEvidence(ctx, id, status, text); err != nil {
			return err
		}
		if p.Opened != nil {
			p.Opened(id, reason)
		}
	}
	return p.Store.Prune(ctx, o.Time, p.Config.Incidents.Retention)
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

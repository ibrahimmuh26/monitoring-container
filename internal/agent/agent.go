// Package agent observes explicitly selected containers. It cannot mutate Docker.
package agent

import (
	"context"
	"errors"
	"time"

	"github.com/ibrahimmuh26/monitoring-container/internal/config"
	"github.com/ibrahimmuh26/monitoring-container/internal/docker"
)

type Observation struct {
	Time         time.Time `json:"time"`
	Server       string    `json:"server"`
	Target       string    `json:"target"`
	ProbeMeaning string    `json:"probe_meaning"`
	// ApplicationHealth stays unknown: a Docker probe is not proof that all
	// application functions and dependencies are healthy.
	ApplicationHealth string          `json:"application_health"`
	Status            string          `json:"observation_status"`
	Container         docker.Snapshot `json:"container"`
}

type Agent struct {
	config    config.Config
	inspector docker.Inspector
	last      map[string]Observation
	handler   func(context.Context, Observation) error
}

// SetHandler installs a per-poll handler, independent of stdout deduplication.
// Set it before running the agent. Errors stop the agent rather than losing evidence.
func (a *Agent) SetHandler(handler func(context.Context, Observation) error) { a.handler = handler }

func New(cfg config.Config, inspector docker.Inspector) *Agent {
	return &Agent{config: cfg, inspector: inspector, last: make(map[string]Observation)}
}

// Scan checks targets sequentially, with a separate deadline for each. Call it
// from one goroutine only. emit is invoked for initial observations and changes.
// Inspection failures are observable data; emit failures abort the scan.
func (a *Agent) Scan(ctx context.Context, emit func(Observation) error) (bool, error) {
	complete := true
	for _, target := range a.config.Targets {
		if !target.Monitoring.Enabled {
			continue
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		checkCtx, cancel := context.WithTimeout(ctx, a.config.Docker.Timeout)
		snapshot, err := a.inspector.Inspect(checkCtx, target.Selector.ContainerName)
		cancel()
		if err := ctx.Err(); err != nil {
			return false, err
		}
		status := "observed"
		if err != nil {
			complete = false
			snapshot = docker.Snapshot{Name: target.Selector.ContainerName, State: "unknown", DockerHealth: "unknown"}
			switch {
			case errors.Is(err, docker.ErrNotFound):
				status = "not_found"
			case errors.Is(err, docker.ErrUnsupported):
				status = "unsupported"
			default:
				status = "unavailable"
			}
		}
		observation := Observation{
			Server: a.config.Server.ID, Target: target.Name,
			ProbeMeaning:      target.Monitoring.DockerHealth.Meaning,
			ApplicationHealth: "unknown", Status: status, Container: snapshot,
		}
		withTime := observation
		withTime.Time = time.Now().UTC()
		if a.handler != nil {
			if err := a.handler(ctx, withTime); err != nil {
				return false, err
			}
		}
		if previous, ok := a.last[target.Name]; ok && previous == observation {
			continue
		}
		// Keep comparison state without timestamps, and update only after delivery.
		if err := emit(withTime); err != nil {
			return false, err
		}
		a.last[target.Name] = observation
	}
	return complete, nil
}

// Run waits the configured interval after a completed scan. Slow Docker calls
// cannot create overlapping scans or an unbounded work queue.
func (a *Agent) Run(ctx context.Context, emit func(Observation) error) error {
	for {
		if _, err := a.Scan(ctx, emit); err != nil {
			return err
		}
		timer := time.NewTimer(a.config.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

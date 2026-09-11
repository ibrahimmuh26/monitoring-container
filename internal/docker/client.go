// Package docker provides a deliberately narrow, read-only Engine API adapter.
// Container inspect and bounded log reads are supported. Minimum API: 1.44 (Engine 25).
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrNotFound = errors.New("target container not found")
var ErrUnavailable = errors.New("Docker inspection unavailable")
var ErrUnsupported = errors.New("Swarm task monitoring is not supported yet")

const maxResponseBytes = 4 << 20

// Snapshot intentionally excludes environment, raw health output, and error strings.
// Do not log raw Docker inspect responses: they can contain credentials.
type Snapshot struct {
	ID           string `json:"container_id,omitempty"`
	Name         string `json:"container_name"`
	ImageID      string `json:"image_id,omitempty"`
	State        string `json:"state"`
	DockerHealth string `json:"docker_health"`
	OOMKilled    bool   `json:"oom_killed"`
	ExitCode     int    `json:"exit_code"`
	RestartCount int    `json:"restart_count"`
	StartedAt    string `json:"started_at,omitempty"`
	TTY          bool   `json:"-"`
}

type Inspector interface {
	Inspect(context.Context, string) (Snapshot, error)
}

type Client struct {
	http      *http.Client
	transport *http.Transport
}

func New(socket string, timeout time.Duration) (*Client, error) {
	if !strings.HasPrefix(socket, "unix:///") || len(socket) <= len("unix:///") || strings.ContainsAny(socket, "\x00\r\n?#") {
		return nil, errors.New("only absolute Unix sockets are supported")
	}
	if timeout <= 0 {
		return nil, errors.New("Docker timeout must be positive")
	}
	path := strings.TrimPrefix(socket, "unix://")
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "unix", path)
		},
		MaxIdleConns:          1,
		MaxIdleConnsPerHost:   1,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: timeout,
	}
	return &Client{
		transport: transport,
		http: &http.Client{
			Transport:     transport,
			Timeout:       timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (c *Client) Close() { c.transport.CloseIdleConnections() }

func (c *Client) Inspect(ctx context.Context, name string) (Snapshot, error) {
	if name == "" || strings.ContainsAny(name, "/\\\x00\r\n") {
		return Snapshot{}, ErrNotFound
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/v1.44/containers/"+url.PathEscape(name)+"/json", nil)
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// Do not expose transport errors that may contain local deployment details.
		return Snapshot{}, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Snapshot{}, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return Snapshot{}, fmt.Errorf("%w (HTTP %d)", ErrUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return Snapshot{}, ErrUnavailable
	}
	var raw struct {
		ID           string `json:"Id"`
		Name         string
		Image        string
		RestartCount int
		Config       struct {
			Labels map[string]string
			Tty    bool
		}
		State *struct {
			Status    string
			OOMKilled bool
			ExitCode  int
			StartedAt string
			Health    *struct{ Status string }
		}
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.ID == "" || raw.State == nil || raw.State.Status == "" {
		return Snapshot{}, ErrUnavailable
	}
	// Engine accepts IDs and ID prefixes as lookup keys. Require an exact name
	// match so a configured name cannot accidentally select a different container.
	if raw.Name != "/"+name {
		return Snapshot{}, ErrNotFound
	}
	if raw.Config.Labels["com.docker.swarm.service.name"] != "" {
		return Snapshot{}, ErrUnsupported
	}
	health := "unknown"
	if raw.State.Health != nil {
		switch raw.State.Health.Status {
		case "starting", "healthy", "unhealthy":
			health = raw.State.Health.Status
		}
	}
	return Snapshot{
		ID: raw.ID, Name: name, ImageID: raw.Image,
		State: raw.State.Status, DockerHealth: health,
		OOMKilled: raw.State.OOMKilled, ExitCode: raw.State.ExitCode,
		RestartCount: raw.RestartCount, StartedAt: raw.State.StartedAt, TTY: raw.Config.Tty,
	}, nil
}

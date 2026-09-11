package docker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// Restart re-inspects the immutable ID captured while opening the incident.
// It never restarts a name-reused replacement, a Swarm task, a running
// container, or a container outside the exited/dead initial recovery scope.
func (c *Client) Restart(ctx context.Context, snapshot Snapshot) error {
	if !fullID.MatchString(snapshot.ID) || snapshot.Name == "" {
		return ErrRestartNotNeeded
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/v1.44/containers/"+snapshot.ID+"/json", nil)
	if err != nil {
		return ErrUnavailable
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return ErrUnavailable
	}
	var raw struct {
		ID     string `json:"Id"`
		Name   string
		Config struct{ Labels map[string]string }
		State  *struct{ Status string }
	}
	if json.Unmarshal(body, &raw) != nil || raw.ID != snapshot.ID || raw.Name != "/"+snapshot.Name || raw.State == nil {
		return ErrRestartNotNeeded
	}
	if raw.Config.Labels["com.docker.swarm.service.name"] != "" {
		return ErrUnsupported
	}
	if raw.State.Status != "exited" && raw.State.Status != "dead" {
		return ErrRestartNotNeeded
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker/v1.44/containers/"+snapshot.ID+"/restart?t=10", nil)
	if err != nil {
		return ErrUnavailable
	}
	response, err := c.http.Do(request)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	// Docker returns 204 on success; no response body is parsed or logged.
	if response.StatusCode == http.StatusNoContent {
		return nil
	}
	if response.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	return ErrUnavailable
}

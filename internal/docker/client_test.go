package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const healthyResponse = `{"Id":"id-1","Name":"/example-api","Image":"sha256:image-1","RestartCount":2,"State":{"Status":"running","Health":{"Status":"healthy"},"StartedAt":"2026-01-01T00:00:00Z"},"Config":{"Env":["PASSWORD=secret"]}}`

func stub(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
	client := &Client{transport: transport, http: &http.Client{
		Transport: transport, Timeout: time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}
	t.Cleanup(client.Close)
	return client
}

func TestInspectReadOnlyAndExactTarget(t *testing.T) {
	client := stub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1.44/containers/example-api/json" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, healthyResponse)
	})
	snapshot, err := client.Inspect(context.Background(), "example-api")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ID != "id-1" || snapshot.DockerHealth != "healthy" || snapshot.RestartCount != 2 {
		t.Fatalf("bad snapshot: %+v", snapshot)
	}
}

func TestInspectionFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"missing", 404, "sensitive daemon message", ErrNotFound},
		{"daemon failure", 500, "sensitive daemon message", ErrUnavailable},
		{"old daemon", 400, "client version too new", ErrUnavailable},
		{"redirect", 302, "", ErrUnavailable},
		{"malformed", 200, "not json", ErrUnavailable},
		{"missing state", 200, `{"Id":"id","Name":"/example-api"}`, ErrUnavailable},
		{"id prefix mismatch", 200, strings.Replace(healthyResponse, "/example-api", "/other", 1), ErrNotFound},
		{"swarm task", 200, `{"Id":"id","Name":"/example-api","Config":{"Labels":{"com.docker.swarm.service.name":"api"}},"State":{"Status":"running"}}`, ErrUnsupported},
		{"oversized", 200, strings.Repeat("x", maxResponseBytes+1), ErrUnavailable},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			client := stub(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "http://example.invalid/secret")
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			})
			_, err := client.Inspect(context.Background(), "example-api")
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
			if strings.Contains(err.Error(), "sensitive") {
				t.Fatal("raw daemon error leaked")
			}
		})
	}
}

func TestNoProbeMeansUnknown(t *testing.T) {
	client := stub(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"Id":"id","Name":"/example-api","State":{"Status":"running"}}`)
	})
	s, err := client.Inspect(context.Background(), "example-api")
	if err != nil || s.DockerHealth != "unknown" {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestCancellation(t *testing.T) {
	client := stub(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.Inspect(ctx, "example-api"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestUnixSocket(t *testing.T) {
	// Short path accommodates macOS Unix socket path length limits.
	dir, err := os.MkdirTemp("/tmp", "mc-socket-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "docker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, healthyResponse)
	}), ReadHeaderTimeout: time.Second}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	client, err := New("unix://"+path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	if _, err := client.Inspect(context.Background(), "example-api"); err != nil {
		t.Fatal(err)
	}
}

func TestRejectNetworkSocket(t *testing.T) {
	if _, err := New("tcp://localhost:2375", time.Second); err == nil {
		t.Fatal("expected rejection")
	}
}

package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckConfigDoesNotNeedDocker(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--config", "../../configs/pilot.example.yaml", "--check-config"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "Configuration valid") {
		t.Fatalf("%d %s %s", code, &stdout, &stderr)
	}
}

func TestInvalidCLI(t *testing.T) {
	for _, args := range [][]string{{"--unknown"}, {"unexpected"}, {"--once", "--check-config"}, {"--config", "does-not-exist.yaml"}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr); code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
}

type cancelWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	w.cancel()
	return n, err
}

func TestContinuousIncidentAndGracefulShutdown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	dataDir := filepath.Join(dir, "data")
	data := fmt.Sprintf(`server:
  id: test-host
docker:
  socket: unix:///nonexistent-monitoring-container-test.sock
incidents:
  enabled: true
  directory: %q
  failure_threshold: 1
targets:
  - name: api
    selector:
      container_name: example-api
    monitoring:
      enabled: true
`, dataDir)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	// One-shot mode must not initialize storage or require Telegram secrets.
	var onceOut, onceErr bytes.Buffer
	run(context.Background(), []string{"--config", path, "--once"}, &onceOut, &onceErr)
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatal("one-shot mode wrote incident state")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &cancelWriter{cancel: cancel}
	var stderr bytes.Buffer
	code := run(ctx, []string{"--config", path}, stdout, &stderr)
	if code != 0 || !strings.Contains(stderr.String(), "Incident opened:") {
		t.Fatalf("%d %s", code, &stderr)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "incidents.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM incidents WHERE reason='inspection_unavailable'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("count %d: %v", count, err)
	}
}

func TestOnceUnavailable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := `server:
  id: test-host
docker:
  socket: unix:///nonexistent-monitoring-container-test.sock
targets:
  - name: example-api
    selector:
      container_name: example-api
    monitoring:
      enabled: true
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--config", path, "--once"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stdout.String(), `"observation_status":"unavailable"`) {
		t.Fatalf("%d %s %s", code, &stdout, &stderr)
	}
}

package incidents

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ibrahimmuh26/monitoring-container/internal/agent"
	"github.com/ibrahimmuh26/monitoring-container/internal/config"
	"github.com/ibrahimmuh26/monitoring-container/internal/diagnostics"
	"github.com/ibrahimmuh26/monitoring-container/internal/docker"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	c, err := config.Decode(strings.NewReader(`server:
  id: test
incidents:
  enabled: true
  failure_threshold: 2
  success_threshold: 2
diagnostics:
  collect_logs: true
targets:
  - name: api
    selector:
      container_name: example-api
    monitoring:
      enabled: true
`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func observation() agent.Observation {
	return agent.Observation{Time: time.Now().UTC(), Server: "test", Target: "api", Status: "observed", ApplicationHealth: "unknown", Container: docker.Snapshot{ID: strings.Repeat("a", 64), Name: "example-api", State: "running", DockerHealth: "unhealthy", StartedAt: "2020-01-01T00:00:00Z"}}
}
func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPersistentThresholdsDedupAndOrderedEvents(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := Open(dir, 8<<20); err == nil {
		other.Close()
		t.Fatal("second owner accepted")
	}
	c := testConfig(t)
	o := observation()
	id, err := s.Record(ctx, o, "docker_unhealthy", false, c.Incidents, "123", time.Minute)
	if err != nil || id != "" {
		t.Fatalf("%s %v", id, err)
	}
	s.Close()
	s, err = Open(dir, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	o.Time = o.Time.Add(time.Second)
	id, err = s.Record(ctx, o, "docker_unhealthy", false, c.Incidents, "123", time.Minute)
	if err != nil || id == "" {
		t.Fatalf("%s %v", id, err)
	}
	if err = s.FinishEvidence(ctx, id, "collected", "safe log\n"); err != nil {
		t.Fatal(err)
	}
	again, err := s.Record(ctx, o, "docker_unhealthy", false, c.Incidents, "123", time.Minute)
	if err != nil || again != "" {
		t.Fatal("duplicate incident", err)
	}
	for n := 0; n < 2; n++ {
		o.Time = o.Time.Add(time.Second)
		if _, err = s.Record(ctx, o, "", true, c.Incidents, "123", time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	e, err := s.NextEvent(ctx, o.Time)
	if err != nil || e.Kind != "opened" {
		t.Fatalf("%+v %v", e, err)
	}
	if err = s.Retry(ctx, e.ID, o.Time.Add(time.Hour), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.NextEvent(ctx, o.Time); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("resolution overtook opening", err)
	}
	if err = s.Delivered(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	e, err = s.NextEvent(ctx, o.Time)
	if err != nil || e.Kind != "condition_cleared" {
		t.Fatalf("%+v %v", e, err)
	}
	i, err := s.Get(ctx, id)
	if err != nil || !i.Closed || i.Logs != "safe log\n" {
		t.Fatalf("%+v %v", i, err)
	}
	if err = s.Prune(ctx, o.Time.Add(48*time.Hour), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(ctx, id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("expired incident retained", err)
	}
}

type fakeLogs struct {
	calls int
	err   error
}

func (f *fakeLogs) Logs(ctx context.Context, s docker.Snapshot, since time.Time, lines, maxBytes int) (string, bool, error) {
	f.calls++
	return "password=do-not-store\nnormal database timeout\n", false, f.err
}
func TestProcessorSanitizesBeforePersistence(t *testing.T) {
	c := testConfig(t)
	s := openTest(t)
	logs := &fakeLogs{}
	r, _ := diagnostics.NewRedactor(nil)
	p := &Processor{Store: s, Config: c, Logs: logs, Redactor: r}
	o := observation()
	ctx := context.Background()
	for n := 0; n < 3; n++ {
		o.Time = o.Time.Add(time.Second)
		if err := p.Handle(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	if logs.calls != 1 {
		t.Fatalf("collected %d times", logs.calls)
	}
	var text, status string
	if err := s.db.QueryRow(`SELECT logs,log_status FROM incidents`).Scan(&text, &status); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "do-not-store") || !strings.Contains(text, "normal database timeout") || status != "collected" {
		t.Fatalf("%q %s", text, status)
	}
	// Telegram was disabled: no surprise backlog is created for a later enable.
	if _, err := s.NextEvent(ctx, o.Time); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}
func TestInterruptedEvidenceAndCollectionFailure(t *testing.T) {
	ctx := context.Background()
	c := testConfig(t)
	c.Incidents.FailureThreshold = 1
	s := openTest(t)
	o := observation()
	id, err := s.Record(ctx, o, "docker_unhealthy", false, c.Incidents, "123", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := diagnostics.NewRedactor(nil)
	p := &Processor{Store: s, Config: c, Logs: &fakeLogs{}, Redactor: r}
	if err = p.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	i, err := s.Get(ctx, id)
	if err != nil || i.LogStatus != "interrupted" {
		t.Fatalf("%+v %v", i, err)
	}
	c2 := c
	c2.Targets[0].Name = "second"
	p.Config = c2
	p.Logs = &fakeLogs{err: errors.New("sensitive transport error")}
	o.Target = "second"
	if err = p.Handle(ctx, o); err != nil {
		t.Fatal(err)
	}
	var status, text string
	if err = s.db.QueryRow(`SELECT log_status,logs FROM incidents WHERE target LIKE '%second%'`).Scan(&status, &text); err != nil {
		t.Fatal(err)
	}
	if status != "unavailable" || text != "" {
		t.Fatal("raw failure persisted")
	}
}
func TestOutboxIDsAndFloodPauseSurviveRetention(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	c := testConfig(t)
	c.Incidents.FailureThreshold = 1
	c.Incidents.SuccessThreshold = 1
	o := observation()
	id, err := s.Record(ctx, o, "docker_unhealthy", false, c.Incidents, "123", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishEvidence(ctx, id, "disabled", ""); err != nil {
		t.Fatal(err)
	}
	e, err := s.NextEvent(ctx, o.Time)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Retry(ctx, e.ID, o.Time.Add(time.Minute), o.Time.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	o.Target = "another"
	id, err = s.Record(ctx, o, "docker_unhealthy", false, c.Incidents, "123", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishEvidence(ctx, id, "disabled", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.NextEvent(ctx, o.Time.Add(time.Second)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("global flood delay bypassed", err)
	}
	// Simulate deletion while an old delivery is in flight. A new event must never
	// reuse that old ID, otherwise its acknowledgement could delete a newer event.
	if _, err = s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	o.Target = "third"
	id, err = s.Record(ctx, o, "docker_unhealthy", false, c.Incidents, "123", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishEvidence(ctx, id, "disabled", ""); err != nil {
		t.Fatal(err)
	}
	next, err := s.NextEvent(ctx, o.Time.Add(2*time.Hour))
	if err != nil || next.ID <= e.ID {
		t.Fatalf("reused event ID: %+v %v", next, err)
	}
}

func TestClassification(t *testing.T) {
	o := observation()
	o.Container.StartedAt = o.Time.Format(time.RFC3339Nano)
	if reason, clear := classify(o, time.Minute); reason != "" || clear {
		t.Fatal("startup reported as incident")
	}
	o.Container.StartedAt = "2020-01-01T00:00:00Z"
	o.ProbeMeaning = "readiness"
	if reason, clear := classify(o, time.Minute); reason != "docker_unhealthy" || clear {
		t.Fatal("unhealthy lost")
	}
	o.Status = "unavailable"
	if reason, _ := classify(o, time.Minute); reason != "inspection_unavailable" {
		t.Fatal("daemon failure mistaken for app failure")
	}
}

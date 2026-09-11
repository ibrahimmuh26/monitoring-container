package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ibrahimmuh26/monitoring-container/internal/config"
	"github.com/ibrahimmuh26/monitoring-container/internal/docker"
)

type fakeInspector struct {
	calls    []string
	snapshot docker.Snapshot
	err      error
}

func (f *fakeInspector) Inspect(ctx context.Context, name string) (docker.Snapshot, error) {
	f.calls = append(f.calls, name)
	return f.snapshot, f.err
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Decode(strings.NewReader(`server:
  id: test-host
targets:
  - name: api
    selector:
      container_name: example-api
    monitoring:
      enabled: true
  - name: excluded
    selector:
      container_name: database
    monitoring:
      enabled: false
`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestTargetIsolationDedupAndReplacement(t *testing.T) {
	fake := &fakeInspector{snapshot: docker.Snapshot{ID: "old", Name: "example-api", State: "running", DockerHealth: "healthy"}}
	agent := New(testConfig(t), fake)
	var observations []Observation
	emit := func(o Observation) error { observations = append(observations, o); return nil }
	for i := 0; i < 2; i++ {
		if ok, err := agent.Scan(context.Background(), emit); !ok || err != nil {
			t.Fatalf("%v %v", ok, err)
		}
	}
	if len(observations) != 1 {
		t.Fatal("duplicate observation emitted")
	}
	if observations[0].ApplicationHealth != "unknown" || observations[0].Time.IsZero() {
		t.Fatal("incorrect health or timestamp")
	}
	fake.snapshot.ID = "new"
	if _, err := agent.Scan(context.Background(), emit); err != nil {
		t.Fatal(err)
	}
	if len(observations) != 2 {
		t.Fatal("replacement not observed")
	}
	for _, name := range fake.calls {
		if name != "example-api" {
			t.Fatalf("unselected target inspected: %s", name)
		}
	}
}

func TestInspectionErrorsAndRecoveryOfObservation(t *testing.T) {
	for _, tt := range []struct {
		err    error
		status string
	}{
		{docker.ErrNotFound, "not_found"},
		{docker.ErrUnavailable, "unavailable"},
		{docker.ErrUnsupported, "unsupported"},
	} {
		t.Run(tt.status, func(t *testing.T) {
			fake := &fakeInspector{err: tt.err}
			a := New(testConfig(t), fake)
			var got Observation
			emit := func(o Observation) error { got = o; return nil }
			ok, err := a.Scan(context.Background(), emit)
			if ok || err != nil || got.Status != tt.status || got.Container.State != "unknown" {
				t.Fatalf("%v %v %+v", ok, err, got)
			}
			fake.err = nil
			fake.snapshot = docker.Snapshot{ID: "new", Name: "example-api", State: "running", DockerHealth: "unknown"}
			ok, err = a.Scan(context.Background(), emit)
			if !ok || err != nil || got.Status != "observed" {
				t.Fatalf("%v %v %+v", ok, err, got)
			}
		})
	}
}

func TestEmitFailureDoesNotDiscardObservation(t *testing.T) {
	a := New(testConfig(t), &fakeInspector{})
	_, err := a.Scan(context.Background(), func(Observation) error { return errors.New("write failed") })
	if err == nil {
		t.Fatal("expected error")
	}
	emitted := false
	_, err = a.Scan(context.Background(), func(Observation) error { emitted = true; return nil })
	if err != nil || !emitted {
		t.Fatal("failed observation was lost")
	}
}

func TestRunStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := New(testConfig(t), &fakeInspector{})
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, func(Observation) error { cancel(); return nil }) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown timed out")
	}
}

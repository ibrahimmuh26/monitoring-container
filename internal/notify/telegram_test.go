package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ibrahimmuh26/monitoring-container/internal/agent"
	"github.com/ibrahimmuh26/monitoring-container/internal/config"
	"github.com/ibrahimmuh26/monitoring-container/internal/incidents"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testTelegram(t *testing.T, logs bool) *Telegram {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	token := "123456:" + strings.Repeat("a", 30)
	if err := os.WriteFile(path, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	sender, err := NewTelegram(path, time.Second, logs)
	if err != nil {
		t.Fatal(err)
	}
	return sender
}
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func TestSummaryDoesNotTransmitLogs(t *testing.T) {
	sender := testTelegram(t, false)
	sender.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.Host != "api.telegram.org" || !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Fatal("invalid request")
		}
		data, _ := io.ReadAll(r.Body)
		if bytes.Contains(data, []byte("private log")) {
			t.Fatal("logs transmitted without opt-in")
		}
		var payload map[string]any
		if json.Unmarshal(data, &payload) != nil || payload["chat_id"] != "-123" {
			t.Fatal("bad payload")
		}
		if _, ok := payload["parse_mode"]; ok {
			t.Fatal("unsafe formatting enabled")
		}
		return response(200, `{"ok":true}`), nil
	})
	_, err := sender.Send(context.Background(), incidents.Event{ChatID: "-123", Kind: "opened"}, incidents.Incident{ID: "INC-test", Logs: "private log"})
	if err != nil {
		t.Fatal(err)
	}
}
func TestDocumentAndRateLimit(t *testing.T) {
	sender := testTelegram(t, true)
	sender.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/sendDocument") {
			t.Fatal("document expected")
		}
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		found := false
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(part)
			if part.FormName() == "document" {
				found = true
				if !bytes.Contains(data, []byte("safe diagnostic")) {
					t.Fatal("missing logs")
				}
			}
		}
		if !found {
			t.Fatal("missing document")
		}
		return response(429, `{"ok":false,"parameters":{"retry_after":30},"description":"sensitive response"}`), nil
	})
	retry, err := sender.Send(context.Background(), incidents.Event{ChatID: "123", Kind: "opened"}, incidents.Incident{ID: "INC-test", Logs: "safe diagnostic"})
	if err == nil || retry != 30*time.Second || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("%s %v", retry, err)
	}
}
func TestTransportErrorNeverExposesToken(t *testing.T) {
	sender := testTelegram(t, false)
	sender.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) { return nil, errors.New(r.URL.String()) })
	_, err := sender.Send(context.Background(), incidents.Event{}, incidents.Incident{})
	if err == nil || strings.Contains(err.Error(), sender.token) {
		t.Fatal("token leaked")
	}
}

type fakeSender struct {
	calls   int
	failure bool
}

func (f *fakeSender) Send(context.Context, incidents.Event, incidents.Incident) (time.Duration, error) {
	f.calls++
	if f.failure {
		return 30 * time.Second, errors.New("offline")
	}
	return 0, nil
}
func TestWorkerPersistentRetryAndAcknowledgement(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := incidents.Open(dir, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	o := agent.Observation{Server: "test", Target: "api", Time: now}
	id, err := store.Record(ctx, o, "inspection_unavailable", false, config.Incidents{FailureThreshold: 1, SuccessThreshold: 1}, "123", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishEvidence(ctx, id, "no_container", ""); err != nil {
		t.Fatal(err)
	}
	fake := &fakeSender{failure: true}
	worker := &Worker{Store: store, Sender: fake}
	delay, err := worker.Tick(ctx, now)
	if err != nil || delay != 30*time.Second || fake.calls != 1 {
		t.Fatalf("%s %v", delay, err)
	}
	store.Close()
	store, err = incidents.Open(dir, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	worker.Store = store
	if _, err = worker.Tick(ctx, now.Add(time.Second)); err != nil || fake.calls != 1 {
		t.Fatal("retry deadline not persisted")
	}
	fake.failure = false
	if _, err = worker.Tick(ctx, now.Add(time.Minute)); err != nil || fake.calls != 2 {
		t.Fatal("retry not delivered", err)
	}
	if _, err = worker.Tick(ctx, now.Add(2*time.Minute)); err != nil || fake.calls != 2 {
		t.Fatal("acknowledged event redelivered", err)
	}
}

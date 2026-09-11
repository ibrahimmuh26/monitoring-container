package docker

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestRestartOnlyImmutableExitedContainer(t *testing.T) {
	id := strings.Repeat("a", 64)
	calls := 0
	client := stub(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			if r.Method != http.MethodGet || r.URL.Path != "/v1.44/containers/"+id+"/json" {
				t.Fatalf("unexpected verification: %s", r.URL)
			}
			fmt.Fprintf(w, `{"Id":%q,"Name":"/example-api","State":{"Status":"exited"}}`, id)
		case 2:
			if r.Method != http.MethodPost || r.URL.Path != "/v1.44/containers/"+id+"/restart" || r.URL.Query().Get("t") != "10" {
				t.Fatalf("unexpected restart: %s", r.URL)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatal("unexpected extra Docker request")
		}
	})
	if err := client.Restart(context.Background(), Snapshot{ID: id, Name: "example-api", State: "exited"}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestRestartFailsClosed(t *testing.T) {
	id := strings.Repeat("b", 64)
	cases := []struct {
		name, body string
		want       error
	}{
		{"running", fmt.Sprintf(`{"Id":%q,"Name":"/example-api","State":{"Status":"running"}}`, id), ErrRestartNotNeeded},
		{"name reused", fmt.Sprintf(`{"Id":%q,"Name":"/replacement","State":{"Status":"exited"}}`, id), ErrRestartNotNeeded},
		{"id mismatch", fmt.Sprintf(`{"Id":%q,"Name":"/example-api","State":{"Status":"exited"}}`, strings.Repeat("c", 64)), ErrRestartNotNeeded},
		{"swarm", fmt.Sprintf(`{"Id":%q,"Name":"/example-api","Config":{"Labels":{"com.docker.swarm.service.name":"api"}},"State":{"Status":"exited"}}`, id), ErrUnsupported},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			client := stub(t, func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, tt.body) })
			err := client.Restart(context.Background(), Snapshot{ID: id, Name: "example-api"})
			if err != tt.want || calls != 1 {
				t.Fatalf("%v calls=%d", err, calls)
			}
		})
	}
	client := stub(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	if err := client.Restart(context.Background(), Snapshot{ID: id, Name: "example-api"}); err != ErrNotFound {
		t.Fatal(err)
	}
	if err := client.Restart(context.Background(), Snapshot{ID: "short", Name: "example-api"}); err != ErrRestartNotNeeded {
		t.Fatal(err)
	}
}

func TestRestartDaemonFailureIsSanitized(t *testing.T) {
	id := strings.Repeat("d", 64)
	calls := 0
	client := stub(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			fmt.Fprintf(w, `{"Id":%q,"Name":"/example-api","State":{"Status":"exited"}}`, id)
			return
		}
		w.WriteHeader(500)
		fmt.Fprint(w, "credential error")
	})
	if err := client.Restart(context.Background(), Snapshot{ID: id, Name: "example-api"}); err != ErrUnavailable {
		t.Fatal(err)
	}
}

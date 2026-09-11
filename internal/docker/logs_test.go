package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func frame(stream byte, text string) []byte {
	b := make([]byte, 8+len(text))
	b[0] = stream
	binary.BigEndian.PutUint32(b[4:8], uint32(len(text)))
	copy(b[8:], text)
	return b
}
func TestLogsReadByImmutableID(t *testing.T) {
	id := strings.Repeat("a", 64)
	client := stub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1.44/containers/"+id+"/logs" || r.URL.Query().Get("follow") != "0" || r.URL.Query().Get("tail") != "20" {
			t.Errorf("bad request: %s", r.URL)
		}
		w.Write(frame(1, "first\n"))
		w.Write(frame(2, "second\n"))
	})
	text, truncated, err := client.Logs(context.Background(), Snapshot{ID: id}, time.Now(), 20, 1024)
	if err != nil || truncated || text != "first\nsecond\n" {
		t.Fatalf("%q %v %v", text, truncated, err)
	}
	if _, _, err := client.Logs(context.Background(), Snapshot{ID: "short-id"}, time.Now(), 20, 1024); err == nil {
		t.Fatal("accepted ambiguous ID")
	}
}
func TestTruncatedLogsOmitPartialLine(t *testing.T) {
	for _, tty := range []bool{false, true} {
		t.Run(fmt.Sprint(tty), func(t *testing.T) {
			client := stub(t, func(w http.ResponseWriter, r *http.Request) {
				text := "safe\nAuthorization: secret\n"
				if tty {
					fmt.Fprint(w, text)
				} else {
					w.Write(frame(1, text))
				}
			})
			text, truncated, err := client.Logs(context.Background(), Snapshot{ID: strings.Repeat("b", 64), TTY: tty}, time.Now(), 20, 10)
			if err != nil || !truncated || text != "safe\n" {
				t.Fatalf("%q %v %v", text, truncated, err)
			}
		})
	}
}
func TestMalformedAndUnsupportedLogs(t *testing.T) {
	for _, body := range [][]byte{[]byte("invalid frame"), append(frame(1, "hello")[:8], byte('x')), bytes.Repeat([]byte{0}, 9)} {
		client := stub(t, func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
		if _, _, err := client.Logs(context.Background(), Snapshot{ID: strings.Repeat("c", 64)}, time.Now(), 20, 1024); err == nil {
			t.Fatal("malformed stream accepted")
		}
	}
	client := stub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, "sensitive driver error")
	})
	if _, _, err := client.Logs(context.Background(), Snapshot{ID: strings.Repeat("c", 64)}, time.Now(), 20, 1024); err != ErrUnavailable {
		t.Fatal(err)
	}
}

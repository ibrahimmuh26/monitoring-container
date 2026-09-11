package diagnostics

import (
	"strings"
	"testing"
)

func TestRedaction(t *testing.T) {
	r, err := NewRedactor([]string{`customer_id=`})
	if err != nil {
		t.Fatal(err)
	}
	input := "Authorization: Bearer abc\n{\"password\":\"hidden\"}\npostgres://user:pass@db/test\ncustomer_id=123\ncontact user@example.com at 192.168.1.2\nrequest 11111111-2222-3333-4444-555555555555\nnormal timeout\n"
	got := r.Clean(input, 4096)
	for _, secret := range []string{"abc", "hidden", "user:pass", "customer_id", "user@example.com", "192.168.1.2", "11111111"} {
		if strings.Contains(got, secret) {
			t.Fatalf("leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "normal timeout") {
		t.Fatal("lost safe diagnostic")
	}
	if len(r.Clean(strings.Repeat("x", 1000), 64)) > 64 {
		t.Fatal("unbounded output")
	}
	if _, err := NewRedactor([]string{"["}); err == nil {
		t.Fatal("invalid regex accepted")
	}
}

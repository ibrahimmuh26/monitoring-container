// Package diagnostics provides conservative, best-effort log sanitization.
package diagnostics

import (
	"regexp"
	"strings"
	"unicode"
)

type Redactor struct{ rules []*regexp.Regexp }

// Entire sensitive lines are omitted, not just the matched token. Unknown
// application-specific personal data still requires custom rules or no collection.
func NewRedactor(patterns []string) (*Redactor, error) {
	defaults := []string{
		`(?i)authorization|password|passwd|\bpwd\b|\btoken\b|secret|api[_-]?key|cookie|\bsession\b|\bbearer\b`,
		`(?i)[a-z][a-z0-9+.-]*://[^\s/]+:[^\s/]+@`,
		`[0-9]{5,}:[A-Za-z0-9_-]{20,}`,
		`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`,
	}
	r := &Redactor{}
	for _, p := range append(defaults, patterns...) {
		rule, err := regexp.Compile(p)
		if err != nil {
			return nil, err
		}
		r.rules = append(r.rules, rule)
	}
	return r, nil
}

var email = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
var ipv4 = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
var uuid = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)

func (r *Redactor) Clean(text string, maxBytes int) string {
	text = strings.ToValidUTF8(text, "")
	text = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) && c != '\n' && c != '\t' {
			return -1
		}
		return c
	}, text)
	var out strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		for _, rule := range r.rules {
			if rule.MatchString(line) {
				line = "[REDACTED LINE]"
				break
			}
		}
		line = email.ReplaceAllString(line, "[EMAIL]")
		line = ipv4.ReplaceAllString(line, "[IP]")
		line = uuid.ReplaceAllString(line, "[ID]")
		if out.Len()+len(line)+1 > maxBytes {
			break
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

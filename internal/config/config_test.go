package config

import (
	"strings"
	"testing"
	"time"
)

const valid = `server:
  id: test-host
targets:
  - name: api
    selector:
      container_name: example-api
    monitoring:
      enabled: true
`

func TestDefaults(t *testing.T) {
	cfg, err := Decode(strings.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Interval != 30*time.Second || cfg.Docker.Timeout != 3*time.Second || cfg.Docker.Socket != "unix:///var/run/docker.sock" {
		t.Fatalf("bad defaults: %+v", cfg)
	}
	if cfg.Targets[0].Recovery.Enabled || cfg.Targets[0].Monitoring.DockerHealth.Meaning != "unknown" {
		t.Fatal("unsafe defaults")
	}
}

func TestRejectInvalidConfigurations(t *testing.T) {
	cases := map[string]string{
		"empty":                            "",
		"unknown key":                      valid + "magic: true\n",
		"telegram without incidents":       valid + "telegram:\n  enabled: true\n",
		"invalid incident threshold":       valid + "incidents:\n  failure_threshold: 0\n",
		"invalid database size":            valid + "incidents:\n  max_bytes: 1\n",
		"unsafe data directory":            valid + "incidents:\n  directory: /\n",
		"unbounded logs":                   valid + "diagnostics:\n  max_bytes: 999999999\n",
		"log collection without incidents": valid + "diagnostics:\n  collect_logs: true\n",
		"invalid redaction":                valid + "diagnostics:\n  redact_patterns: ['[']\n",
		"send logs without consent":        valid + "telegram:\n  send_logs: true\n",
		"multiple documents":               valid + "---\nserver: {}\n",
		"empty second document":            valid + "---\n",
		"duplicate yaml key":               valid + "server:\n  id: duplicate\n",
		"invalid server":                   strings.Replace(valid, "test-host", "host/name", 1),
		"path selector":                    strings.Replace(valid, "example-api", "../api", 1),
		"wildcard selector":                strings.Replace(valid, "example-api", "api*", 1),
		"network socket":                   valid + "docker:\n  socket: tcp://localhost:2375\n",
		"empty socket":                     valid + "docker:\n  socket: unix:///\n",
		"bad timeout":                      valid + "docker:\n  timeout: 0s\n",
		"bad interval":                     valid + "interval: 0s\n",
		"bad duration":                     valid + "interval: yesterday\n",
		"no enabled targets":               strings.Replace(valid, "enabled: true", "enabled: false", 1),
		"unsupported recovery":             valid + "    recovery:\n      enabled: true\n",
		"unknown probe meaning":            valid + "      docker_health:\n        meaning: magic\n",
		"duplicate target":                 valid + "  - name: api\n    selector:\n      container_name: another-api\n",
		"duplicate container":              valid + "  - name: second\n    selector:\n      container_name: example-api\n",
		"oversized file":                   strings.Repeat(" ", maxConfigBytes+1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(input)); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestExampleConfiguration(t *testing.T) {
	for _, name := range []string{"pilot", "incidents"} {
		if _, err := Load("../../configs/" + name + ".example.yaml"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMissingFile(t *testing.T) {
	if _, err := Load(t.TempDir() + "/missing.yaml"); err == nil {
		t.Fatal("expected error")
	}
}

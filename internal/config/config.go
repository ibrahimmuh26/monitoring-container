// Package config loads the supported observation-only configuration.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const maxConfigBytes = 1 << 20

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

type Config struct {
	Server      Server        `yaml:"server"`
	Docker      Docker        `yaml:"docker"`
	Interval    time.Duration `yaml:"interval"`
	Targets     []Target      `yaml:"targets"`
	Incidents   Incidents     `yaml:"incidents"`
	Diagnostics Diagnostics   `yaml:"diagnostics"`
	Telegram    Telegram      `yaml:"telegram"`
}

type Incidents struct {
	Enabled          bool          `yaml:"enabled"`
	Directory        string        `yaml:"directory"`
	FailureThreshold int           `yaml:"failure_threshold"`
	SuccessThreshold int           `yaml:"success_threshold"`
	StartupGrace     time.Duration `yaml:"startup_grace"`
	Retention        time.Duration `yaml:"retention"`
	MaxBytes         int64         `yaml:"max_bytes"`
}

type Diagnostics struct {
	CollectLogs    bool          `yaml:"collect_logs"`
	Timeout        time.Duration `yaml:"timeout"`
	LogWindow      time.Duration `yaml:"log_window"`
	MaxLines       int           `yaml:"max_lines"`
	MaxBytes       int           `yaml:"max_bytes"`
	RedactPatterns []string      `yaml:"redact_patterns"`
}

type Telegram struct {
	Enabled   bool          `yaml:"enabled"`
	TokenFile string        `yaml:"token_file"`
	ChatID    string        `yaml:"chat_id"`
	SendLogs  bool          `yaml:"send_logs"`
	Timeout   time.Duration `yaml:"timeout"`
}

type Server struct {
	ID string `yaml:"id"`
}

type Docker struct {
	Socket  string        `yaml:"socket"`
	Timeout time.Duration `yaml:"timeout"`
}

type Target struct {
	Name       string     `yaml:"name"`
	Selector   Selector   `yaml:"selector"`
	Monitoring Monitoring `yaml:"monitoring"`
	Recovery   Recovery   `yaml:"recovery"`
}

type Selector struct {
	ContainerName string `yaml:"container_name"`
}

type Monitoring struct {
	Enabled      bool         `yaml:"enabled"`
	DockerHealth DockerHealth `yaml:"docker_health"`
}

type DockerHealth struct {
	Meaning string `yaml:"meaning"`
}

type Recovery struct {
	Enabled                bool          `yaml:"enabled"`
	Timeout                time.Duration `yaml:"timeout"`
	Cooldown               time.Duration `yaml:"cooldown"`
	MaxAttemptsPerIncident int           `yaml:"max_attempts_per_incident"`
	MaxAttemptsPerHour     int           `yaml:"max_attempts_per_hour"`
}

func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open configuration: %w", err)
	}
	defer f.Close()
	return Decode(f)
}

func Decode(r io.Reader) (Config, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxConfigBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}
	if len(data) > maxConfigBytes {
		return Config{}, errors.New("configuration exceeds 1 MiB")
	}
	cfg := Config{Interval: 30 * time.Second, Docker: Docker{
		Socket: "unix:///var/run/docker.sock", Timeout: 3 * time.Second,
	}, Incidents: Incidents{
		Directory: "/var/lib/monitoring-container", FailureThreshold: 3, SuccessThreshold: 3,
		StartupGrace: time.Minute, Retention: 7 * 24 * time.Hour, MaxBytes: 64 << 20,
	}, Diagnostics: Diagnostics{Timeout: 5 * time.Second, LogWindow: 10 * time.Minute, MaxLines: 200, MaxBytes: 32 << 10},
		Telegram: Telegram{Timeout: 10 * time.Second}}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Config{}, errors.New("configuration must contain exactly one YAML document")
	}
	for i := range cfg.Targets {
		if cfg.Targets[i].Recovery.Timeout == 0 {
			cfg.Targets[i].Recovery.Timeout = 20 * time.Second
		}
		if cfg.Targets[i].Recovery.Cooldown == 0 {
			cfg.Targets[i].Recovery.Cooldown = 5 * time.Minute
		}
		if cfg.Targets[i].Recovery.MaxAttemptsPerIncident == 0 {
			cfg.Targets[i].Recovery.MaxAttemptsPerIncident = 1
		}
		if cfg.Targets[i].Recovery.MaxAttemptsPerHour == 0 {
			cfg.Targets[i].Recovery.MaxAttemptsPerHour = 2
		}
		if cfg.Targets[i].Monitoring.DockerHealth.Meaning == "" {
			cfg.Targets[i].Monitoring.DockerHealth.Meaning = "unknown"
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if !identifier.MatchString(c.Server.ID) {
		return errors.New("server.id must be a 1-128 character identifier")
	}
	if !strings.HasPrefix(c.Docker.Socket, "unix:///") || len(c.Docker.Socket) <= len("unix:///") || strings.ContainsAny(c.Docker.Socket, "\x00\r\n?#") {
		return errors.New("docker.socket must be an absolute unix:/// socket path")
	}
	if c.Docker.Timeout < time.Millisecond*100 || c.Docker.Timeout > 30*time.Second {
		return errors.New("docker.timeout must be between 100ms and 30s")
	}
	if c.Interval < time.Second || c.Interval > time.Hour {
		return errors.New("interval must be between 1s and 1h")
	}
	if len(c.Targets) == 0 || len(c.Targets) > 100 {
		return errors.New("configure between 1 and 100 targets")
	}
	names, containers := map[string]bool{}, map[string]bool{}
	enabled := 0
	for i, target := range c.Targets {
		if !identifier.MatchString(target.Name) || !identifier.MatchString(target.Selector.ContainerName) {
			return fmt.Errorf("targets[%d]: name and container_name must be 1-128 character identifiers", i)
		}
		if names[target.Name] || containers[target.Selector.ContainerName] {
			return fmt.Errorf("targets[%d]: duplicate name or container selector", i)
		}
		names[target.Name], containers[target.Selector.ContainerName] = true, true
		if target.Recovery.Enabled {
			if !c.Incidents.Enabled {
				return fmt.Errorf("targets[%d]: recovery requires incidents.enabled", i)
			}
			if target.Recovery.Timeout < time.Second || target.Recovery.Timeout > time.Minute || target.Recovery.Cooldown < time.Minute || target.Recovery.Cooldown > 24*time.Hour || target.Recovery.MaxAttemptsPerIncident != 1 || target.Recovery.MaxAttemptsPerHour < 1 || target.Recovery.MaxAttemptsPerHour > 10 {
				return fmt.Errorf("targets[%d]: recovery supports one attempt per incident, timeout 1s-1m, cooldown 1m-24h, and 1-10 attempts/hour", i)
			}
		}
		switch target.Monitoring.DockerHealth.Meaning {
		case "unknown", "liveness", "readiness":
		default:
			return fmt.Errorf("targets[%d]: docker_health.meaning must be unknown, liveness, or readiness", i)
		}
		if target.Monitoring.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return errors.New("at least one target must have monitoring.enabled: true")
	}
	if !filepath.IsAbs(c.Incidents.Directory) || filepath.Clean(c.Incidents.Directory) == "/" {
		return errors.New("incidents.directory must be an absolute dedicated data directory, not root")
	}
	if c.Incidents.FailureThreshold < 1 || c.Incidents.FailureThreshold > 100 || c.Incidents.SuccessThreshold < 1 || c.Incidents.SuccessThreshold > 100 {
		return errors.New("incident thresholds must be between 1 and 100")
	}
	if c.Incidents.StartupGrace < 0 || c.Incidents.StartupGrace > time.Hour || c.Incidents.Retention < time.Hour || c.Incidents.Retention > 90*24*time.Hour || c.Incidents.MaxBytes < 8<<20 || c.Incidents.MaxBytes > 1<<30 {
		return errors.New("invalid incident grace, retention (1h-90d), or database limit (8MiB-1GiB)")
	}
	if c.Diagnostics.Timeout < 100*time.Millisecond || c.Diagnostics.Timeout > 30*time.Second || c.Diagnostics.LogWindow < time.Second || c.Diagnostics.LogWindow > time.Hour || c.Diagnostics.MaxLines < 1 || c.Diagnostics.MaxLines > 2000 || c.Diagnostics.MaxBytes < 1024 || c.Diagnostics.MaxBytes > 128<<10 {
		return errors.New("invalid diagnostic bounds")
	}
	if len(c.Diagnostics.RedactPatterns) > 20 {
		return errors.New("at most 20 custom redaction patterns are allowed")
	}
	for _, pattern := range c.Diagnostics.RedactPatterns {
		if len(pattern) > 1024 {
			return errors.New("redaction pattern too long")
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return errors.New("invalid redaction pattern")
		}
	}
	if c.Diagnostics.CollectLogs && !c.Incidents.Enabled {
		return errors.New("log collection requires incidents.enabled")
	}
	if c.Telegram.Timeout < time.Second || c.Telegram.Timeout > 30*time.Second {
		return errors.New("telegram.timeout must be between 1s and 30s")
	}
	if c.Telegram.Enabled {
		if !c.Incidents.Enabled {
			return errors.New("Telegram requires incidents.enabled")
		}
		if !filepath.IsAbs(c.Telegram.TokenFile) {
			return errors.New("telegram.token_file must be absolute")
		}
		if !regexp.MustCompile(`^-?[1-9][0-9]{0,19}$`).MatchString(c.Telegram.ChatID) {
			return errors.New("telegram.chat_id must be a numeric chat ID")
		}
	}
	if c.Telegram.SendLogs && (!c.Telegram.Enabled || !c.Diagnostics.CollectLogs) {
		return errors.New("telegram.send_logs requires Telegram and log collection")
	}
	return nil
}

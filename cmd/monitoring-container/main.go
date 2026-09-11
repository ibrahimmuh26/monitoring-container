package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ibrahimmuh26/monitoring-container/internal/agent"
	"github.com/ibrahimmuh26/monitoring-container/internal/config"
	"github.com/ibrahimmuh26/monitoring-container/internal/diagnostics"
	"github.com/ibrahimmuh26/monitoring-container/internal/docker"
	"github.com/ibrahimmuh26/monitoring-container/internal/incidents"
	"github.com/ibrahimmuh26/monitoring-container/internal/notify"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("monitoring-container", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", "/etc/monitoring-container/config.yaml", "configuration path")
	once := flags.Bool("once", false, "inspect enabled targets once and exit")
	check := flags.Bool("check-config", false, "validate configuration without contacting Docker")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || (*once && *check) {
		fmt.Fprintln(stderr, "unexpected arguments or conflicting modes")
		return 2
	}
	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *check {
		fmt.Fprintln(stdout, "Configuration valid. One-shot mode performs no incident handling or recovery.")
		return 0
	}
	client, err := docker.New(cfg.Docker.Socket, cfg.Docker.Timeout)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	defer client.Close()
	observer := agent.New(cfg, client)
	encoder := json.NewEncoder(stdout)
	emit := func(observation agent.Observation) error { return encoder.Encode(observation) }
	fmt.Fprintln(stderr, "Starting Docker monitoring agent. Recovery follows per-target policy.")
	if *once {
		complete, err := observer.Scan(ctx, emit)
		if err != nil || !complete {
			fmt.Fprintln(stderr, "Inspection incomplete; check observation_status and Docker access.")
			return 1
		}
		return 0
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workerDone chan error
	if cfg.Incidents.Enabled {
		store, err := incidents.Open(cfg.Incidents.Directory, cfg.Incidents.MaxBytes)
		if err != nil {
			fmt.Fprintln(stderr, "Cannot initialize incident store:", err)
			return 1
		}
		defer store.Close()
		redactor, err := diagnostics.NewRedactor(cfg.Diagnostics.RedactPatterns)
		if err != nil {
			fmt.Fprintln(stderr, "Invalid redaction configuration")
			return 2
		}
		processor := &incidents.Processor{Store: store, Config: cfg, Logs: client, Restart: client, Redactor: redactor,
			Opened: func(id, reason string) { fmt.Fprintln(stderr, "Incident opened:", id, "condition:", reason) },
		}
		if err := processor.Resume(runCtx); err != nil {
			fmt.Fprintln(stderr, "Cannot resume incident evidence:", err)
			return 1
		}
		observer.SetHandler(processor.Handle)
		if cfg.Telegram.Enabled {
			sender, err := notify.NewTelegram(cfg.Telegram.TokenFile, cfg.Telegram.Timeout, cfg.Telegram.SendLogs)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 2
			}
			worker := &notify.Worker{Store: store, Sender: sender, Warn: func(message string) { fmt.Fprintln(stderr, message) }}
			workerDone = make(chan error, 1)
			go func() { workerDone <- worker.Run(runCtx); cancel() }()
		}
	}
	err = observer.Run(runCtx, emit)
	cancel()
	if workerDone != nil {
		if workerErr := <-workerDone; workerErr != nil && !errors.Is(workerErr, context.Canceled) {
			fmt.Fprintln(stderr, "Notification worker stopped:", workerErr)
			return 1
		}
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, "Agent stopped:", err)
		return 1
	}
	return 0
}

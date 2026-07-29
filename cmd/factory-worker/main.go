package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/owainlewis/factory/internal/worker"
)

var version = "dev"

func main() {
	if worker.IsSupervisorCommand(os.Args[1:]) {
		control := os.NewFile(3, "factory-worker-control")
		if err := worker.RunSupervisor(control, os.Stdin, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "factory-worker supervisor:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "factory-worker:", err)
		os.Exit(1)
	}
}

func run() error {
	defaultConfig, err := worker.DefaultConfigPath()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("factory-worker", flag.ContinueOnError)
	configPath := flags.String("config", defaultConfig, "Factory V2 worker TOML configuration path")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	config, err := worker.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	manager, err := worker.New(config, worker.Options{WorkerVersion: version}, logger)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), worker.ShutdownSignals()...)
	defer stop()
	logger.Info("worker_started", "worker_id", manager.ID(), "name", config.Name)
	if err := manager.Run(ctx); err != nil {
		return err
	}
	logger.Info("worker_stopped", "worker_id", manager.ID())
	return nil
}

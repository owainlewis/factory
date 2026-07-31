package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/owainlewis/factory/internal/poller"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "factory-poller:", err)
		os.Exit(1)
	}
}

func run() error {
	defaultConfig, err := poller.DefaultConfigPath()
	if err != nil {
		return err
	}
	configPath := flag.String("config", defaultConfig, "Factory poller TOML configuration path")
	once := flag.Bool("once", false, "poll each queue once and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flag.Args())
	}
	config, err := poller.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	engine, err := poller.New(ctx, config, logger)
	if err != nil {
		return err
	}
	defer engine.Close()
	if *once {
		summary, err := engine.RunOnce(ctx)
		if encodeErr := json.NewEncoder(os.Stdout).Encode(summary); err == nil {
			err = encodeErr
		}
		return err
	}
	return engine.Run(ctx)
}

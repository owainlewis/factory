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
	testGitHub := flag.Bool("test-github", false, "test GitHub queue matching without creating tasks")
	queueName := flag.String("queue", "", "test only this configured GitHub queue")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flag.Args())
	}
	if *once && *testGitHub {
		return fmt.Errorf("-once and -test-github cannot be used together")
	}
	if *queueName != "" && !*testGitHub {
		return fmt.Errorf("-queue requires -test-github")
	}
	config, err := poller.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *testGitHub {
		report, testErr := poller.TestGitHub(ctx, config, *queueName)
		if encodeErr := json.NewEncoder(os.Stdout).Encode(report); testErr == nil {
			testErr = encodeErr
		}
		return testErr
	}
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

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/owainlewis/factory/internal/runner"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	opts, rest, err := parseArgs(args)
	if err != nil {
		return err
	}

	app, err := runner.New(opts.ConfigPath)
	if err != nil {
		return err
	}

	if len(rest) == 0 {
		return usage()
	}

	switch rest[0] {
	case "repos":
		return app.ListRepos(os.Stdout)
	case "workflows":
		if len(rest) != 2 {
			return fmt.Errorf("usage: factory workflows <repo>")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return app.ListWorkflows(ctx, os.Stdout, rest[1])
	case "run":
		if len(rest) < 2 {
			return fmt.Errorf("usage: factory run <repo> [workflow] [--mode plan|execute]")
		}
		workflow := "hello"
		if len(rest) >= 3 {
			workflow = rest[2]
		}
		mode, err := runner.ParseMode(opts.Mode)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		record, err := app.Run(ctx, rest[1], workflow, mode)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "run %s %s\n", record.ID, record.Status)
		fmt.Fprintf(os.Stdout, "log %s\n", record.LogPath)
		fmt.Fprintf(os.Stdout, "record %s\n", record.RecordPath)
		return nil
	case "runs":
		return app.ListRuns(os.Stdout)
	default:
		return usage()
	}
}

type options struct {
	ConfigPath string
	Mode       string
}

func parseArgs(args []string) (options, []string, error) {
	opts := options{ConfigPath: "config.yaml", Mode: string(runner.ModePlan)}
	rest := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config", "-c":
			if i+1 >= len(args) {
				return opts, nil, fmt.Errorf("--config requires a path")
			}
			opts.ConfigPath = args[i+1]
			i++
		case "--mode":
			if i+1 >= len(args) {
				return opts, nil, fmt.Errorf("--mode requires plan or execute")
			}
			opts.Mode = args[i+1]
			i++
		default:
			rest = append(rest, args[i])
		}
	}

	return opts, rest, nil
}

func usage() error {
	return fmt.Errorf("usage: factory [--config config.yaml] <repos|run|runs|workflows>")
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/owainlewis/factory/internal/controlplane"
	"github.com/owainlewis/factory/internal/statepath"
	factoryweb "github.com/owainlewis/factory/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "factory-server:", err)
		os.Exit(1)
	}
}

func run() (returnErr error) {
	defaultDatabase, dataRoot, err := defaultDatabasePath()
	if err != nil {
		return err
	}
	listen := flag.String("listen", "127.0.0.1:7337", "loopback HTTP listen address")
	database := flag.String("database", defaultDatabase, "Factory SQLite database path")
	flag.Parse()

	listenAddress, err := controlplane.ResolveListenAddress(*listen)
	if err != nil {
		return err
	}
	databaseExplicit := false
	flag.Visit(func(value *flag.Flag) {
		if value.Name == "database" {
			databaseExplicit = true
		}
	})
	if err := validateLegacyServerSelection(
		factoryDataHome(),
		databaseExplicit,
		dataRoot,
	); err != nil {
		return err
	}
	if *database == defaultDatabase {
		if err := validateDataRoot(dataRoot); err != nil {
			return err
		}
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := controlplane.Open(rootContext, *database)
	if err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("close SQLite: %w", err)
		}
	}()
	expired, err := store.SweepExpired(rootContext)
	if err != nil {
		return fmt.Errorf("startup lease sweep: %w", err)
	}
	if len(expired) > 0 {
		logger.Info("startup_leases_expired", "attempt_count", len(expired))
	}
	for _, lease := range expired {
		logger.Info("state_change",
			"resource_type", "attempt",
			"resource_id", lease.AttemptID,
			"execution_id", lease.ExecutionID,
			"new_state", "lost",
		)
	}
	sweepContext, cancelSweep := context.WithCancel(rootContext)
	sweeperDone := make(chan struct{})
	go func() {
		defer close(sweeperDone)
		store.RunSweeper(sweepContext, logger)
	}()
	defer func() {
		cancelSweep()
		<-sweeperDone
	}()
	automationService := controlplane.NewAutomationService(store, logger)
	automationContext, cancelAutomations := context.WithCancel(rootContext)
	automationsDone := make(chan struct{})
	go func() {
		defer close(automationsDone)
		automationService.Run(automationContext)
	}()
	defer func() {
		automationService.StopAdmission()
		cancelAutomations()
		<-automationsDone
	}()

	listener, err := net.ListenTCP("tcp", listenAddress)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	handler := factoryweb.NewHandler(controlplane.NewHandlerWithAutomation(store, logger, automationService))
	server := controlplane.NewHTTPServer(*listen, handler)
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("server_started",
			"address", listener.Addr().String(),
			"database", *database,
			"ui_url", "http://"+listener.Addr().String()+"/",
		)
		serverErrors <- server.Serve(listener)
	}()

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			automationService.StopAdmission()
			cancelAutomations()
			<-automationsDone
			cancelSweep()
			<-sweeperDone
			return fmt.Errorf("serve HTTP: %w", err)
		}
	case <-rootContext.Done():
		automationService.StopAdmission()
		cancelAutomations()
		<-automationsDone
		cancelSweep()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			return fmt.Errorf("shut down HTTP server: %w", err)
		}
		if err := <-serverErrors; !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
	}
	cancelSweep()
	<-sweeperDone
	cancelAutomations()
	<-automationsDone
	logger.Info("server_stopped")
	return nil
}

func defaultDatabasePath() (database string, root string, err error) {
	root = factoryDataHome()
	if root == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", "", fmt.Errorf("resolve home directory: %w", homeErr)
		}
		root = filepath.Join(home, ".factory")
	}
	return filepath.Join(root, "server", "factory.sqlite3"), root, nil
}

func validateNoLegacyServerDefault(newRoot string) error {
	if _, found, err := findPreviewServerState(newRoot); err != nil {
		return err
	} else if found {
		return nil
	}
	legacyRoot := filepath.Join(filepath.Dir(newRoot), ".factory-v2")
	legacyState, found, err := findPreviewServerState(legacyRoot)
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf(
			"found preview control-plane state at %s; refusing to abandon durable tasks for the new default; set FACTORY_DATA_HOME=%s to keep using it, or archive the old state after resolving its work",
			legacyState,
			legacyRoot,
		)
	}
	return nil
}

func validateLegacyServerSelection(dataHome string, databaseExplicit bool, newRoot string) error {
	if dataHome != "" || databaseExplicit {
		return nil
	}
	return validateNoLegacyServerDefault(newRoot)
}

func findPreviewServerState(root string) (string, bool, error) {
	database := filepath.Join(root, "server", "factory.sqlite3")
	for _, candidate := range []string{database, database + ".v2-control-plane"} {
		if _, err := os.Lstat(candidate); err == nil {
			return candidate, true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", false, fmt.Errorf("inspect preview control-plane state %s: %w", candidate, err)
		}
	}
	return "", false, nil
}

func validateDataRoot(root string) error {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve Factory data root: %w", err)
	}
	if marker, found, err := statepath.FindRetiredDatabaseMarker(absolute); err != nil {
		return err
	} else if found {
		return fmt.Errorf("refusing a Factory data root below retired local state at %s", marker)
	}
	canonical, err := statepath.CanonicalProspective(absolute)
	if err != nil {
		return fmt.Errorf("canonicalize Factory data root: %w", err)
	}
	if marker, found, err := statepath.FindRetiredDatabaseMarker(canonical); err != nil {
		return err
	} else if found {
		return fmt.Errorf("refusing a Factory data root below retired local state at %s", marker)
	}
	return nil
}

func factoryDataHome() string {
	if value := os.Getenv("FACTORY_DATA_HOME"); value != "" {
		return value
	}
	return os.Getenv("FACTORY_V2_DATA_HOME")
}

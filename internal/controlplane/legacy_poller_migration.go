package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/owainlewis/factory/internal/protocol"
	_ "modernc.org/sqlite"
)

const maxLegacyPollerConfigBytes = 1 << 20

var legacyIssueKeyPattern = regexp.MustCompile(`^#([0-9]{1,10})$`)
var legacyQueueNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
var legacySourcePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,49}$`)

type legacyPollerConfig struct {
	Server        string              `toml:"server"`
	PollEvery     string              `toml:"poll_every"`
	DataDirectory string              `toml:"data_directory"`
	Queues        []legacyPollerQueue `toml:"queues"`
}

type legacyPollerQueue struct {
	Name           string   `toml:"name"`
	Source         string   `toml:"source"`
	Command        []string `toml:"command"`
	Project        string   `toml:"project"`
	Status         string   `toml:"status"`
	Labels         []string `toml:"labels"`
	WorkerID       string   `toml:"worker_id"`
	RepositoryKey  string   `toml:"repository_key"`
	Prompt         string   `toml:"prompt"`
	TimeoutSeconds int      `toml:"timeout_seconds"`
}

type legacyPollerObservation struct {
	QueueID    string `json:"queue_id"`
	IssueKey   string `json:"issue_key"`
	RequestKey string `json:"request_key"`
	Request    []byte `json:"request_json"`
	TaskID     string `json:"task_id"`
	State      string `json:"state"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

type legacyPollerSnapshot struct {
	ConfigBytes      []byte                    `json:"config_bytes"`
	ConfigPath       string                    `json:"config_path"`
	DataHome         string                    `json:"data_home"`
	WorkingDirectory string                    `json:"working_directory"`
	DataDirectory    string                    `json:"data_directory"`
	LedgerPath       string                    `json:"ledger_path"`
	LedgerIdentity   string                    `json:"ledger_identity"`
	LedgerSHA256     string                    `json:"ledger_sha256"`
	ArchiveRoot      string                    `json:"archive_root"`
	Schema           []legacySchemaEntry       `json:"schema"`
	Observations     []legacyPollerObservation `json:"observations"`
	Config           legacyPollerConfig        `json:"-"`
	Digest           string                    `json:"-"`
}

type legacySchemaEntry struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Table string `json:"table"`
	SQL   string `json:"sql"`
}

type legacyLockedSource struct {
	db       *sql.DB
	tx       *sql.Tx
	ledger   *os.File
	snapshot legacyPollerSnapshot
}

func (source *legacyLockedSource) close() error {
	var result error
	if source.tx != nil {
		result = source.tx.Rollback()
		if errors.Is(result, sql.ErrTxDone) {
			result = nil
		}
	}
	if source.db != nil {
		result = errors.Join(result, source.db.Close())
	}
	if source.ledger != nil {
		result = errors.Join(result, source.ledger.Close())
	}
	return result
}

func (source *legacyLockedSource) verifyLedgerPath() error {
	pathInfo, err := os.Lstat(source.snapshot.LedgerPath)
	if err != nil {
		return conflict("migration_source_changed", "legacy ledger path changed during the locked action; stop factory-poller and run Preview again")
	}
	openInfo, err := source.ledger.Stat()
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(pathInfo, openInfo) {
		return conflict("migration_source_changed", "legacy ledger path changed during the locked action; stop factory-poller and run Preview again")
	}
	return nil
}

func (s *Store) PreviewLegacyPoller(
	ctx context.Context,
	input protocol.PreviewLegacyPollerRequest,
) (protocol.LegacyPollerMigration, error) {
	if !input.ConfirmStopped {
		return protocol.LegacyPollerMigration{}, invalid(
			"legacy_poller_confirmation_required",
			"stop factory-poller and confirm it is stopped before Preview",
		)
	}
	if active, err := s.ActiveLegacyPollerMigration(ctx); err != nil {
		return protocol.LegacyPollerMigration{}, err
	} else if active != nil {
		return protocol.LegacyPollerMigration{}, conflict(
			"migration_in_progress",
			"legacy poller migration "+active.ID+" is already imported; Resume or Skip pending observations and Finalize it before starting another Preview",
		)
	}
	source, err := openLegacyPollerSource(ctx, input.LegacyPollerSelection)
	if err != nil {
		return protocol.LegacyPollerMigration{}, err
	}
	defer source.close()

	migrationID, err := newID()
	if err != nil {
		return protocol.LegacyPollerMigration{}, unavailable(err)
	}
	now := s.now().UnixMilli()
	preview, err := s.legacyPollerMigrationFromSnapshot(ctx, migrationID, "previewed", source.snapshot)
	if err != nil {
		return protocol.LegacyPollerMigration{}, err
	}
	var unimportedPending, unimportedSubmitted int
	for _, queue := range preview.Queues {
		if !queue.Supported {
			unimportedPending += queue.PendingObservations
			unimportedSubmitted += queue.SubmittedObservations
		}
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO legacy_poller_migrations(
			id, snapshot_digest, config_path, data_home, working_directory,
			data_directory, ledger_path, archive_root, status, queue_count,
			supported_queue_count, unsupported_queue_count,
			unimported_pending_observation_count, unimported_submitted_observation_count,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'previewed', ?, ?, ?, ?, ?, ?, ?)
	`, migrationID, source.snapshot.Digest, source.snapshot.ConfigPath,
		source.snapshot.DataHome, source.snapshot.WorkingDirectory,
		source.snapshot.DataDirectory, source.snapshot.LedgerPath,
		source.snapshot.ArchiveRoot, preview.Counts.Queues, preview.Counts.Supported,
		preview.Counts.Unsupported, unimportedPending, unimportedSubmitted, now, now); err != nil {
		return protocol.LegacyPollerMigration{}, unavailable(err)
	}
	preview.CreatedAt, preview.UpdatedAt = fromMillis(now), fromMillis(now)
	return preview, nil
}

func openLegacyPollerSource(
	ctx context.Context,
	selection protocol.LegacyPollerSelection,
) (*legacyLockedSource, error) {
	initial, err := resolveLegacyPollerSelection(selection)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(initial.LedgerPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, invalid("legacy_ledger_not_found", "legacy poller ledger was not found at "+initial.LedgerPath)
	}
	if err != nil {
		return nil, unavailable(fmt.Errorf("inspect legacy poller ledger: %w", err))
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, invalid("invalid_legacy_ledger", "legacy poller ledger must be a regular non-symlink file")
	}
	ledger, err := os.Open(initial.LedgerPath)
	if err != nil {
		return nil, unavailable(fmt.Errorf("open legacy poller ledger snapshot: %w", err))
	}
	ledgerInfo, err := ledger.Stat()
	if err != nil || !os.SameFile(info, ledgerInfo) {
		ledger.Close()
		return nil, conflict("migration_source_changed", "legacy poller ledger changed while acquiring the lock; stop factory-poller and run Preview again")
	}
	databaseURL := &url.URL{Scheme: "file", Path: initial.LedgerPath}
	dsn := databaseURL.String() + "?_pragma=busy_timeout%280%29&_txlock=exclusive"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		ledger.Close()
		return nil, unavailable(fmt.Errorf("open legacy poller ledger: %w", err))
	}
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		db.Close()
		ledger.Close()
		if isSQLiteContention(err) {
			return nil, conflict("legacy_poller_active", "could not acquire the exclusive legacy ledger lock; stop factory-poller and retry")
		}
		return nil, unavailable(fmt.Errorf("lock legacy poller ledger: %w", err))
	}
	source := &legacyLockedSource{db: db, tx: tx, ledger: ledger, snapshot: initial}
	if err := source.verifyLedgerPath(); err != nil {
		source.close()
		return nil, err
	}
	snapshot, err := readLegacyPollerSnapshot(ctx, tx, ledger, selection)
	if err != nil {
		source.close()
		return nil, err
	}
	if snapshot.ConfigPath != initial.ConfigPath || snapshot.DataHome != initial.DataHome ||
		snapshot.WorkingDirectory != initial.WorkingDirectory || snapshot.DataDirectory != initial.DataDirectory ||
		snapshot.LedgerPath != initial.LedgerPath || snapshot.ArchiveRoot != initial.ArchiveRoot {
		source.close()
		return nil, conflict("migration_source_changed", "legacy poller source paths changed while acquiring the lock; stop factory-poller and run Preview again")
	}
	source.snapshot = snapshot
	if err := source.verifyLedgerPath(); err != nil {
		source.close()
		return nil, err
	}
	return source, nil
}

func resolveLegacyPollerSelection(selection protocol.LegacyPollerSelection) (legacyPollerSnapshot, error) {
	workingDirectory := strings.TrimSpace(selection.WorkingDirectory)
	if workingDirectory == "" {
		var err error
		workingDirectory, err = os.Getwd()
		if err != nil {
			return legacyPollerSnapshot{}, unavailable(fmt.Errorf("resolve server working directory: %w", err))
		}
	}
	if !filepath.IsAbs(workingDirectory) {
		return legacyPollerSnapshot{}, invalid("invalid_working_directory", "working_directory must be absolute")
	}
	dataHome := strings.TrimSpace(selection.DataHome)
	if dataHome == "" {
		dataHome = os.Getenv("FACTORY_DATA_HOME")
		if dataHome == "" {
			dataHome = os.Getenv("FACTORY_V2_DATA_HOME")
		}
		if dataHome == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return legacyPollerSnapshot{}, unavailable(fmt.Errorf("resolve home directory: %w", err))
			}
			dataHome = filepath.Join(home, ".factory")
		}
	}
	if !filepath.IsAbs(dataHome) {
		dataHome = filepath.Join(workingDirectory, dataHome)
	}
	dataHome, _ = filepath.Abs(dataHome)
	configPath := strings.TrimSpace(selection.ConfigPath)
	if configPath == "" {
		configPath = os.Getenv("FACTORY_POLLER_CONFIG")
	}
	if configPath == "" {
		configPath = filepath.Join(dataHome, "poller.toml")
	} else if !filepath.IsAbs(configPath) {
		configPath = filepath.Join(workingDirectory, configPath)
	}
	configPath, _ = filepath.Abs(configPath)
	configBytes, config, err := readLegacyPollerConfig(configPath)
	if err != nil {
		return legacyPollerSnapshot{}, err
	}
	dataDirectory := strings.TrimSpace(config.DataDirectory)
	if dataDirectory == "" {
		dataDirectory = filepath.Join(dataHome, "poller")
	} else if !filepath.IsAbs(dataDirectory) {
		dataDirectory = filepath.Join(filepath.Dir(configPath), dataDirectory)
	}
	dataDirectory, _ = filepath.Abs(dataDirectory)
	return legacyPollerSnapshot{
		ConfigBytes: configBytes, Config: config, ConfigPath: configPath,
		DataHome: dataHome, WorkingDirectory: workingDirectory,
		DataDirectory: dataDirectory, LedgerPath: filepath.Join(dataDirectory, "poller.sqlite3"),
		ArchiveRoot: filepath.Join(dataHome, "archive", "poller"),
	}, nil
}

func readLegacyPollerConfig(path string) ([]byte, legacyPollerConfig, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, legacyPollerConfig{}, invalid("legacy_config_not_found", "legacy poller configuration was not found at "+path)
	}
	if err != nil {
		return nil, legacyPollerConfig{}, unavailable(fmt.Errorf("inspect legacy poller configuration: %w", err))
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, legacyPollerConfig{}, invalid("invalid_legacy_config", "legacy poller configuration must be a regular non-symlink file")
	}
	if info.Size() > maxLegacyPollerConfigBytes {
		return nil, legacyPollerConfig{}, invalid("legacy_config_too_large", "legacy poller configuration exceeds 1 MiB")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, legacyPollerConfig{}, unavailable(fmt.Errorf("read legacy poller configuration: %w", err))
	}
	var config legacyPollerConfig
	metadata, err := toml.Decode(string(body), &config)
	if err != nil {
		return nil, legacyPollerConfig{}, invalid("invalid_legacy_config", "decode legacy poller configuration: "+err.Error())
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		sort.Strings(keys)
		return nil, legacyPollerConfig{}, invalid("invalid_legacy_config", "unknown legacy poller configuration fields: "+strings.Join(keys, ", "))
	}
	if strings.TrimSpace(config.PollEvery) == "" {
		config.PollEvery = "30s"
	}
	interval, err := time.ParseDuration(config.PollEvery)
	if err != nil || interval < 10*time.Second || interval > 24*time.Hour {
		return nil, legacyPollerConfig{}, invalid("invalid_legacy_config", "poll_every must be a duration between 10s and 24h")
	}
	if len(config.Queues) == 0 {
		return nil, legacyPollerConfig{}, invalid("invalid_legacy_config", "legacy poller configuration has no queues")
	}
	names := make(map[string]bool, len(config.Queues))
	for index := range config.Queues {
		queue := &config.Queues[index]
		queue.Name = strings.TrimSpace(queue.Name)
		queue.Source = strings.ToLower(strings.TrimSpace(queue.Source))
		queue.Project = strings.TrimSpace(queue.Project)
		queue.Status = strings.TrimSpace(queue.Status)
		if queue.TimeoutSeconds == 0 {
			queue.TimeoutSeconds = int(protocol.DefaultTimeout.Seconds())
		}
		if !legacyQueueNamePattern.MatchString(queue.Name) || names[queue.Name] {
			return nil, legacyPollerConfig{}, invalid("invalid_legacy_config", fmt.Sprintf("queue %d has an invalid or duplicate name", index+1))
		}
		names[queue.Name] = true
		if !legacySourcePattern.MatchString(queue.Source) || queue.Project == "" || len(queue.Project) > 500 ||
			queue.Status == "" || len(queue.Status) > 200 {
			return nil, legacyPollerConfig{}, invalid("invalid_legacy_config", "queue "+queue.Name+" has invalid source, project, or status")
		}
		if queue.Source == "github" && len(queue.Command) != 0 {
			return nil, legacyPollerConfig{}, invalid("invalid_legacy_config", "GitHub queue "+queue.Name+" must not configure a command")
		}
		if queue.Source != "github" && (len(queue.Command) == 0 || strings.TrimSpace(queue.Command[0]) == "") {
			return nil, legacyPollerConfig{}, invalid("invalid_legacy_config", "command queue "+queue.Name+" must configure an executable")
		}
		if len(queue.Labels) > 20 {
			return nil, legacyPollerConfig{}, invalid("invalid_legacy_config", "queue "+queue.Name+" has more than 20 labels")
		}
		seenLabels := make(map[string]bool, len(queue.Labels))
		for _, label := range queue.Labels {
			key := label
			if queue.Source == "github" {
				key = strings.ToLower(label)
			}
			if label == "" || label != strings.TrimSpace(label) || len(label) > 200 || seenLabels[key] {
				return nil, legacyPollerConfig{}, invalid("invalid_legacy_config", "queue "+queue.Name+" has an invalid or duplicate label")
			}
			seenLabels[key] = true
		}
		if strings.TrimSpace(queue.Prompt) == "" || len([]byte(queue.Prompt)) > protocol.MaxDescriptionBytes/2 {
			return nil, legacyPollerConfig{}, invalid("invalid_legacy_config", "queue "+queue.Name+" has an invalid prompt")
		}
		if queue.TimeoutSeconds < 1 || queue.TimeoutSeconds > int(protocol.MaxTimeout.Seconds()) {
			return nil, legacyPollerConfig{}, invalid("invalid_legacy_config", "queue "+queue.Name+" has an invalid timeout_seconds")
		}
	}
	return body, config, nil
}

func readLegacyPollerSnapshot(
	ctx context.Context,
	tx *sql.Tx,
	ledger *os.File,
	selection protocol.LegacyPollerSelection,
) (legacyPollerSnapshot, error) {
	snapshot, err := resolveLegacyPollerSelection(selection)
	if err != nil {
		return snapshot, err
	}
	var journalMode string
	if err := tx.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		return snapshot, invalid("invalid_legacy_ledger", "read legacy ledger journal mode: "+err.Error())
	}
	if !strings.EqualFold(journalMode, "delete") {
		return snapshot, invalid("invalid_legacy_ledger", "legacy poller ledger must use DELETE journal mode before migration")
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT type, name, tbl_name, COALESCE(sql, '')
		FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'
		ORDER BY type, name, tbl_name
	`)
	if err != nil {
		return snapshot, invalid("invalid_legacy_ledger", "read legacy ledger schema: "+err.Error())
	}
	for rows.Next() {
		var entry legacySchemaEntry
		if err := rows.Scan(&entry.Type, &entry.Name, &entry.Table, &entry.SQL); err != nil {
			rows.Close()
			return snapshot, invalid("invalid_legacy_ledger", "scan legacy ledger schema: "+err.Error())
		}
		snapshot.Schema = append(snapshot.Schema, entry)
	}
	if err := rows.Close(); err != nil {
		return snapshot, invalid("invalid_legacy_ledger", "close legacy ledger schema: "+err.Error())
	}
	observationRows, err := tx.QueryContext(ctx, `
		SELECT queue_id, issue_key, request_key, request_json, task_id, state,
		       created_at, updated_at
		FROM observations ORDER BY created_at, queue_id, issue_key
	`)
	if err != nil {
		return snapshot, invalid("invalid_legacy_ledger", "legacy ledger does not have the supported observations schema: "+err.Error())
	}
	for observationRows.Next() {
		var observation legacyPollerObservation
		if err := observationRows.Scan(
			&observation.QueueID, &observation.IssueKey, &observation.RequestKey,
			&observation.Request, &observation.TaskID, &observation.State,
			&observation.CreatedAt, &observation.UpdatedAt,
		); err != nil {
			observationRows.Close()
			return snapshot, invalid("invalid_legacy_ledger", "scan legacy observation: "+err.Error())
		}
		if observation.State != "pending" && observation.State != "submitted" {
			observationRows.Close()
			return snapshot, invalid("invalid_legacy_ledger", "legacy observation has unsupported state "+observation.State)
		}
		snapshot.Observations = append(snapshot.Observations, observation)
	}
	if err := observationRows.Close(); err != nil {
		return snapshot, invalid("invalid_legacy_ledger", "close legacy observations: "+err.Error())
	}
	configAgain, err := os.ReadFile(snapshot.ConfigPath)
	if err != nil || !bytes.Equal(configAgain, snapshot.ConfigBytes) {
		return snapshot, conflict("migration_source_changed", "legacy poller configuration changed during the locked action; stop factory-poller and run Preview again")
	}
	ledgerInfo, err := ledger.Stat()
	if err != nil {
		return snapshot, unavailable(fmt.Errorf("inspect locked legacy ledger: %w", err))
	}
	identity, err := legacyFileIdentity(ledgerInfo)
	if err != nil {
		return snapshot, unavailable(err)
	}
	ledgerDigest, err := digestOpenFile(ledger, ledgerInfo.Size())
	if err != nil {
		return snapshot, unavailable(fmt.Errorf("digest locked legacy ledger: %w", err))
	}
	snapshot.LedgerIdentity = identity
	snapshot.LedgerSHA256 = ledgerDigest
	body, err := json.Marshal(snapshot)
	if err != nil {
		return snapshot, unavailable(err)
	}
	digest := sha256.Sum256(body)
	snapshot.Digest = hex.EncodeToString(digest[:])
	return snapshot, nil
}

func legacyFileIdentity(info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("legacy ledger filesystem identity is unavailable")
	}
	return fmt.Sprintf("%d:%d", uint64(stat.Dev), uint64(stat.Ino)), nil
}

func legacyQueueID(queue legacyPollerQueue) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{queue.Name, queue.Source, queue.Project}, "\x00")))
	return hex.EncodeToString(digest[:24])
}

func legacyIssueNumber(issueKey string) (int, error) {
	match := legacyIssueKeyPattern.FindStringSubmatch(strings.TrimSpace(issueKey))
	if len(match) != 2 {
		return 0, fmt.Errorf("issue key %q is not a GitHub #number identity", issueKey)
	}
	number, err := strconv.Atoi(match[1])
	if err != nil || number < 1 || int64(number) > int64(1<<31-1) {
		return 0, fmt.Errorf("issue key %q is outside the supported GitHub range", issueKey)
	}
	return number, nil
}

func (s *Store) legacyPollerMigrationFromSnapshot(
	ctx context.Context,
	migrationID string,
	status string,
	snapshot legacyPollerSnapshot,
) (protocol.LegacyPollerMigration, error) {
	result := protocol.LegacyPollerMigration{
		ID: migrationID, SnapshotDigest: snapshot.Digest, Status: status,
		ConfigPath: snapshot.ConfigPath, DataHome: snapshot.DataHome,
		WorkingDirectory: snapshot.WorkingDirectory, DataDirectory: snapshot.DataDirectory,
		LedgerPath: snapshot.LedgerPath, ArchiveRoot: snapshot.ArchiveRoot,
		Queues: []protocol.LegacyPollerQueue{}, Automations: []protocol.Automation{},
		Occurrences: []protocol.AutomationOccurrence{}, Errors: []string{},
	}
	counts := make(map[string]protocol.LegacyPollerCounts)
	for _, observation := range snapshot.Observations {
		value := counts[observation.QueueID]
		if observation.State == "pending" {
			value.Pending++
		} else {
			value.Submitted++
		}
		counts[observation.QueueID] = value
	}
	repositories, err := s.ManagedRepositories(ctx)
	if err != nil {
		return result, err
	}
	byIdentity := make(map[string]protocol.ManagedRepository)
	for _, repository := range repositories {
		byIdentity[strings.ToLower(repository.RemoteIdentity)] = repository
	}
	existingWorkflowNames := make(map[string]bool)
	workflowRows, err := s.db.QueryContext(ctx, `SELECT current_name_key FROM workflows`)
	if err != nil {
		return result, unavailable(err)
	}
	for workflowRows.Next() {
		var key string
		if err := workflowRows.Scan(&key); err != nil {
			workflowRows.Close()
			return result, unavailable(err)
		}
		existingWorkflowNames[key] = true
	}
	if err := workflowRows.Close(); err != nil {
		return result, unavailable(err)
	}
	existingAutomationNames := make(map[string]bool)
	automationRows, err := s.db.QueryContext(ctx, `SELECT name_key FROM automations`)
	if err != nil {
		return result, unavailable(err)
	}
	for automationRows.Next() {
		var key string
		if err := automationRows.Scan(&key); err != nil {
			automationRows.Close()
			return result, unavailable(err)
		}
		existingAutomationNames[key] = true
	}
	if err := automationRows.Close(); err != nil {
		return result, unavailable(err)
	}
	configuredQueueIDs := make(map[string]bool, len(snapshot.Config.Queues))
	for _, queue := range snapshot.Config.Queues {
		queueID := legacyQueueID(queue)
		configuredQueueIDs[queueID] = true
		queueCounts := counts[queueID]
		item := protocol.LegacyPollerQueue{
			QueueID: queueID, Name: queue.Name, Source: queue.Source, Project: queue.Project,
			State: queue.Status, RequiredLabels: append([]string(nil), queue.Labels...),
			TimeoutSeconds: queue.TimeoutSeconds,
			WorkflowName:   queue.Name + " workflow", AutomationName: queue.Name,
			PendingObservations: queueCounts.Pending, SubmittedObservations: queueCounts.Submitted,
			Errors: []string{},
		}
		interval, intervalErr := time.ParseDuration(snapshot.Config.PollEvery)
		if intervalErr == nil {
			item.PollIntervalSeconds = int(interval.Seconds())
		}
		fatal := false
		if queue.Source != "github" {
			item.Errors = append(item.Errors, "Only built-in GitHub queues can be imported; this command queue remains recoverable in the archive.")
			fatal = true
		} else {
			identity, normalizeErr := normalizeManagedGitHubRemote("github.com/" + strings.TrimSuffix(queue.Project, ".git"))
			if normalizeErr != nil {
				item.Errors = append(item.Errors, "Project is not a valid GitHub owner/repository.")
				fatal = true
			} else if repository, found := byIdentity[strings.ToLower(identity)]; !found {
				item.Errors = append(item.Errors, "No matching managed repository exists for "+identity+".")
				fatal = true
			} else {
				item.RepositoryID, item.RepositoryIdentity = repository.ID, repository.RemoteIdentity
			}
			if queue.Status != "open" && queue.Status != "closed" {
				item.Errors = append(item.Errors, "GitHub issue state must be open or closed.")
				fatal = true
			}
			if strings.TrimSpace(queue.Prompt) == "" || len([]byte(queue.Prompt)) > protocol.MaxWorkflowInstructionsBytes {
				item.Errors = append(item.Errors, "Queue prompt must be nonblank and no larger than 48 KiB.")
				fatal = true
			}
			for _, observation := range snapshot.Observations {
				if observation.QueueID != queueID {
					continue
				}
				if _, issueErr := legacyIssueNumber(observation.IssueKey); issueErr != nil {
					item.Errors = append(item.Errors, issueErr.Error())
					fatal = true
					break
				}
				if observation.State == "pending" && item.RepositoryIdentity != "" {
					if _, requestErr := validateLegacyPendingRequest(observation, item.RepositoryIdentity); requestErr != nil {
						item.Errors = append(item.Errors, "Pending observation "+observation.IssueKey+" cannot be resumed and requires Skip: "+requestErr.Error())
					}
				}
			}
			if existingWorkflowNames[normalizeWorkflowName(item.WorkflowName)] {
				item.Errors = append(item.Errors, "Default Workflow name already exists; choose an explicit rename before Import.")
			}
			if existingAutomationNames[normalizeWorkflowName(item.AutomationName)] {
				item.Errors = append(item.Errors, "Default Automation name already exists; choose an explicit rename before Import.")
			}
		}
		item.Supported = !fatal
		result.Queues = append(result.Queues, item)
		result.Counts.Queues++
		result.Counts.Pending += item.PendingObservations
		result.Counts.Submitted += item.SubmittedObservations
		if item.Supported {
			result.Counts.Supported++
		} else {
			result.Counts.Unsupported++
		}
		for _, message := range item.Errors {
			result.Errors = append(result.Errors, queue.Name+": "+message)
		}
	}
	missingQueueIDs := make([]string, 0)
	for queueID := range counts {
		if !configuredQueueIDs[queueID] {
			missingQueueIDs = append(missingQueueIDs, queueID)
		}
	}
	sort.Strings(missingQueueIDs)
	for _, queueID := range missingQueueIDs {
		queueCounts := counts[queueID]
		message := "Ledger observations reference queue ID " + queueID + " which is missing from poller.toml; restore the matching queue before Import."
		result.Queues = append(result.Queues, protocol.LegacyPollerQueue{
			QueueID: queueID, Name: "Removed legacy queue " + queueID, Source: "ledger-only",
			PendingObservations: queueCounts.Pending, SubmittedObservations: queueCounts.Submitted,
			Supported: false, Blocking: true, Errors: []string{message},
		})
		result.Counts.Queues++
		result.Counts.Unsupported++
		result.Counts.Pending += queueCounts.Pending
		result.Counts.Submitted += queueCounts.Submitted
		result.Errors = append(result.Errors, message)
	}
	return result, nil
}

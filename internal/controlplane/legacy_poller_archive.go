package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

type legacyPollerArchiveManifest struct {
	MigrationID    string `json:"migration_id"`
	SnapshotDigest string `json:"snapshot_digest"`
	ConfigSource   string `json:"config_source"`
	LedgerSource   string `json:"ledger_source"`
	ConfigSHA256   string `json:"config_sha256"`
	LedgerSHA256   string `json:"ledger_sha256"`
}

var legacyArchiveRename = os.Rename
var legacyArchiveSyncDirectory = syncDirectory

func (s *Store) FinalizeLegacyPoller(
	ctx context.Context,
	input protocol.FinalizeLegacyPollerRequest,
) (protocol.LegacyPollerMigration, error) {
	if !input.ConfirmStopped {
		return protocol.LegacyPollerMigration{}, invalid(
			"legacy_poller_confirmation_required",
			"stop factory-poller and confirm it is stopped before Finalize",
		)
	}
	input.MigrationID = strings.TrimSpace(input.MigrationID)
	input.SnapshotDigest = strings.TrimSpace(input.SnapshotDigest)
	if input.MigrationID == "" || input.SnapshotDigest == "" {
		return protocol.LegacyPollerMigration{}, invalid("invalid_migration", "migration_id and snapshot_digest are required")
	}
	source, err := openLegacyPollerSource(ctx, input.LegacyPollerSelection)
	if err != nil {
		return protocol.LegacyPollerMigration{}, err
	}
	defer source.close()
	migration, err := s.verifyLegacyMigrationBinding(ctx, input.MigrationID, input.SnapshotDigest, source.snapshot)
	if err != nil {
		return protocol.LegacyPollerMigration{}, err
	}
	if migration.Status == "previewed" {
		return protocol.LegacyPollerMigration{}, conflict("migration_not_imported", "Import the reviewed legacy queues before Finalize")
	}
	var unresolved int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM legacy_poller_observations legacy
		JOIN automation_occurrences occurrence ON occurrence.id = legacy.occurrence_id
		WHERE legacy.migration_id = ?
		  AND (occurrence.state IN ('pending', 'dispatching', 'failed')
		       OR occurrence.legacy_task_request_json IS NOT NULL)
	`, migration.ID).Scan(&unresolved); err != nil {
		return protocol.LegacyPollerMigration{}, unavailable(err)
	}
	if unresolved != 0 {
		return protocol.LegacyPollerMigration{}, conflict(
			"migration_pending_observations",
			fmt.Sprintf("%d imported pending observations still require Resume or Skip", unresolved),
		)
	}
	archivePath, err := createLegacyPollerArchive(source, migration.ID)
	if err != nil {
		var serviceErr *ServiceError
		if errors.As(err, &serviceErr) {
			return protocol.LegacyPollerMigration{}, err
		}
		return protocol.LegacyPollerMigration{}, &ServiceError{
			Code: "migration_archive_failed", Message: "could not complete the recoverable legacy archive", Status: 503, Err: err,
		}
	}
	now := s.now().UnixMilli()
	if _, err := s.db.ExecContext(ctx, `
		UPDATE legacy_poller_migrations
		SET status = 'finalized', archive_path = ?, finalized_at = COALESCE(finalized_at, ?), updated_at = ?
		WHERE id = ? AND snapshot_digest = ? AND status IN ('imported', 'finalized')
	`, archivePath, now, now, migration.ID, migration.SnapshotDigest); err != nil {
		return protocol.LegacyPollerMigration{}, unavailable(err)
	}
	return s.LegacyPollerMigration(ctx, migration.ID)
}

func createLegacyPollerArchive(source *legacyLockedSource, migrationID string) (string, error) {
	snapshot := source.snapshot
	if err := source.verifyLedgerPath(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(snapshot.ArchiveRoot, 0o700); err != nil {
		return "", fmt.Errorf("create archive root: %w", err)
	}
	if err := os.Chmod(snapshot.ArchiveRoot, 0o700); err != nil {
		return "", fmt.Errorf("protect archive root: %w", err)
	}
	finalPath := filepath.Join(snapshot.ArchiveRoot, migrationID)
	stagingPath := filepath.Join(snapshot.ArchiveRoot, "."+migrationID+".staging")
	configDigest := sha256.Sum256(snapshot.ConfigBytes)
	manifest := legacyPollerArchiveManifest{
		MigrationID: migrationID, SnapshotDigest: snapshot.Digest,
		ConfigSource: snapshot.ConfigPath, LedgerSource: snapshot.LedgerPath,
		ConfigSHA256: hex.EncodeToString(configDigest[:]), LedgerSHA256: snapshot.LedgerSHA256,
	}
	if info, err := os.Lstat(finalPath); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("archive collision at " + finalPath)
		}
		if err := verifyLegacyArchive(finalPath, manifest); err != nil {
			return "", fmt.Errorf("archive collision at %s: %w", finalPath, err)
		}
		if err := legacyArchiveSyncDirectory(snapshot.ArchiveRoot); err != nil {
			return "", fmt.Errorf("sync recovered archive parent: %w", err)
		}
		return finalPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect final archive: %w", err)
	}
	if info, err := os.Lstat(stagingPath); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("archive staging collision at " + stagingPath)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(stagingPath, 0o700); err != nil {
			return "", fmt.Errorf("create archive staging directory: %w", err)
		}
	} else {
		return "", fmt.Errorf("inspect archive staging directory: %w", err)
	}
	if err := ensureArchiveBytes(filepath.Join(stagingPath, "poller.toml"), snapshot.ConfigBytes, manifest.ConfigSHA256); err != nil {
		return "", err
	}
	if err := ensureArchiveCopy(filepath.Join(stagingPath, "poller.sqlite3"), source.ledger, manifest.LedgerSHA256); err != nil {
		return "", err
	}
	if err := source.verifyLedgerPath(); err != nil {
		return "", err
	}
	manifestBody, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	manifestBody = append(manifestBody, '\n')
	manifestDigest := sha256.Sum256(manifestBody)
	if err := ensureArchiveBytes(filepath.Join(stagingPath, "manifest.json"), manifestBody, hex.EncodeToString(manifestDigest[:])); err != nil {
		return "", err
	}
	if err := legacyArchiveSyncDirectory(stagingPath); err != nil {
		return "", fmt.Errorf("sync archive staging directory: %w", err)
	}
	if err := source.verifyLedgerPath(); err != nil {
		return "", err
	}
	if err := legacyArchiveRename(stagingPath, finalPath); err != nil {
		if verifyErr := verifyLegacyArchive(finalPath, manifest); verifyErr == nil {
			if syncErr := legacyArchiveSyncDirectory(snapshot.ArchiveRoot); syncErr != nil {
				return "", fmt.Errorf("sync recovered archive parent: %w", syncErr)
			}
			return finalPath, nil
		}
		return "", fmt.Errorf("publish archive: %w", err)
	}
	if err := legacyArchiveSyncDirectory(snapshot.ArchiveRoot); err != nil {
		return "", fmt.Errorf("sync archive parent: %w", err)
	}
	return finalPath, nil
}

func ensureArchiveBytes(path string, body []byte, expectedDigest string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("archive staging file must be a regular non-symlink file: " + path)
		}
		digest, digestErr := digestFile(path)
		if digestErr == nil && digest == expectedDigest {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("replace invalid archive staging file: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect archive staging file: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create archive file: %w", err)
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		return fmt.Errorf("write archive file: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync archive file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close archive file: %w", err)
	}
	return nil
}

func ensureArchiveCopy(path string, source *os.File, expectedDigest string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("archive staging ledger must be a regular non-symlink file")
		}
		digest, digestErr := digestFile(path)
		if digestErr == nil && digest == expectedDigest {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("replace invalid archive staging ledger: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect archive staging ledger: %w", err)
	}
	info, err := source.Stat()
	if err != nil {
		return fmt.Errorf("inspect locked legacy ledger for archive: %w", err)
	}
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create archived ledger: %w", err)
	}
	if _, err := io.Copy(output, io.NewSectionReader(source, 0, info.Size())); err != nil {
		output.Close()
		return fmt.Errorf("copy locked legacy ledger: %w", err)
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return fmt.Errorf("sync archived ledger: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close archived ledger: %w", err)
	}
	digest, err := digestFile(path)
	if err != nil || digest != expectedDigest {
		return errors.New("archived ledger did not match the locked source")
	}
	return nil
}

func verifyLegacyArchive(path string, expected legacyPollerArchiveManifest) error {
	for _, name := range []string{"manifest.json", "poller.toml", "poller.sqlite3"} {
		info, err := os.Lstat(filepath.Join(path, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("archive contains a missing or non-regular file: " + name)
		}
	}
	body, err := os.ReadFile(filepath.Join(path, "manifest.json"))
	if err != nil {
		return err
	}
	var actual legacyPollerArchiveManifest
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&actual); err != nil {
		return err
	}
	if actual != expected {
		return errors.New("archive manifest does not match this migration snapshot")
	}
	configDigest, err := digestFile(filepath.Join(path, "poller.toml"))
	if err != nil || configDigest != expected.ConfigSHA256 {
		return errors.New("archived configuration does not match its manifest")
	}
	ledgerDigest, err := digestFile(filepath.Join(path, "poller.sqlite3"))
	if err != nil || ledgerDigest != expected.LedgerSHA256 {
		return errors.New("archived ledger does not match its manifest")
	}
	return nil
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func digestOpenFile(file *os.File, size int64) (string, error) {
	digest := sha256.New()
	if _, err := io.Copy(digest, io.NewSectionReader(file, 0, size)); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

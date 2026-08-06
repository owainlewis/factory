package worker

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

func (manager *Manager) setHealth(value health) {
	manager.stateMutex.Lock()
	previous := manager.health.State
	if manager.fatalHealth != nil {
		value = health{State: "unhealthy", Error: manager.fatalHealth}
	}
	manager.health = value
	if previous != value.State {
		manager.registered = false
	}
	if value.State != "healthy" {
		manager.cancelPendingClaimsLocked()
	}
	manager.stateMutex.Unlock()
	if value.Error != nil && previous != value.State {
		manager.logger.Warn("worker_unhealthy", "error_class", "runtime_health", "error", value.Error)
	}
	if value.State == "healthy" && previous != "healthy" {
		manager.logger.Info("worker_healthy",
			"git_version", value.GitVersion,
			"runtime", manager.config.Runtime,
			"runtime_version", value.RuntimeVersion)
	}
}

func (manager *Manager) markUnhealthy(errorClass string, err error) {
	manager.stateMutex.Lock()
	manager.fatalHealth = err
	manager.health.State = "unhealthy"
	manager.health.Error = err
	manager.registered = false
	manager.cancelPendingClaimsLocked()
	manager.stateMutex.Unlock()
	manager.logger.Error("worker_unhealthy", "error_class", errorClass, "error", err)
}

func (manager *Manager) isHealthy() bool {
	manager.stateMutex.Lock()
	defer manager.stateMutex.Unlock()
	return manager.health.State == "healthy" && manager.registered
}

func (manager *Manager) registration() protocol.WorkerRegistration {
	manager.stateMutex.Lock()
	defer manager.stateMutex.Unlock()
	repositories := make([]protocol.RepositoryRegistration, 0, len(manager.repositories))
	retained := make([]protocol.RetainedWorktree, 0, len(manager.retained))
	disposedAttemptIDs := make([]string, 0, len(manager.disposed))
	managedRepositoryIDs := make([]string, 0, len(manager.managedRepositoryIDs))
	for _, value := range manager.retained {
		retained = append(retained, value)
	}
	for attemptID := range manager.disposed {
		disposedAttemptIDs = append(disposedAttemptIDs, attemptID)
	}
	for repositoryID := range manager.managedRepositoryIDs {
		managedRepositoryIDs = append(managedRepositoryIDs, repositoryID)
	}
	sort.Strings(managedRepositoryIDs)
	for _, repository := range manager.repositories {
		repositories = append(repositories, protocol.RepositoryRegistration{
			Key: repository.Key, RemoteIdentity: repository.RemoteIdentity,
			RetainedCount: manager.retainedCounts[repository.RemoteIdentity],
		})
	}
	acceptsManagedRepositories := hasGitHubSourceAccess(manager.health.SourceAccess)
	return protocol.WorkerRegistration{
		Name:                       strings.TrimSpace(manager.config.Name),
		WorkerVersion:              manager.options.WorkerVersion,
		Runtime:                    manager.config.Runtime,
		RuntimeVersion:             manager.health.RuntimeVersion,
		Capacity:                   manager.config.MaxConcurrent,
		ActiveCount:                len(manager.slots),
		Health:                     manager.health.State,
		Repositories:               repositories,
		SourceAccess:               append([]protocol.SourceAccess(nil), manager.health.SourceAccess...),
		AcceptsManagedRepositories: acceptsManagedRepositories,
		ManagedRepositoryIDs:       managedRepositoryIDs,
		RetainedWorktrees:          retained,
		CapacityHandoffVersion:     1,
		DisposedAttemptIDs:         disposedAttemptIDs,
		WeeklyLimit:                manager.health.WeeklyLimit,
	}
}

func hasGitHubSourceAccess(values []protocol.SourceAccess) bool {
	for _, value := range values {
		if value.Provider == "github" && value.Hostname == "github.com" {
			return true
		}
	}
	return false
}

func (manager *Manager) register(ctx context.Context) {
	manager.registrationMutex.Lock()
	defer manager.registrationMutex.Unlock()
	if manager.capacityHandoffs != 0 {
		return
	}
	manager.registerLocked(ctx)
}

func (manager *Manager) registerLocked(ctx context.Context) {
	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	registration := manager.registration()
	if _, err := manager.client.register(requestContext, manager.id, registration); err != nil {
		manager.stateMutex.Lock()
		manager.registered = false
		manager.cancelPendingClaimsLocked()
		manager.stateMutex.Unlock()
		var apiError *APIError
		if errors.As(err, &apiError) && apiError.Status < 500 {
			manager.markUnhealthy("worker_registration", errors.New("worker configuration was rejected by the control plane"))
		}
		manager.logger.Warn("worker_registration_failed", "error_class", apiErrorClass(err))
		return
	}
	if err := manager.manifests.removeDisposedManifests(registration.DisposedAttemptIDs); err != nil {
		manager.markUnhealthy("attempt_manifest", err)
		return
	}
	if err := manager.manifests.clearDisposals(registration.DisposedAttemptIDs); err != nil {
		manager.markUnhealthy("disposal_journal", err)
		return
	}
	manager.stateMutex.Lock()
	for _, attemptID := range registration.DisposedAttemptIDs {
		delete(manager.disposed, attemptID)
	}
	manager.registered = true
	manager.stateMutex.Unlock()
}

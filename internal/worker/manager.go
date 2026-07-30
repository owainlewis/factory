package worker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

const (
	defaultPollInterval         = 2 * time.Second
	defaultHealthInterval       = 30 * time.Second
	defaultRegistrationInterval = 10 * time.Second
	defaultLeaseRenewInterval   = 10 * time.Second
	defaultLeaseRetryInterval   = 2 * time.Second
	defaultShutdownTimeout      = 30 * time.Second
	defaultWorkerVersion        = "dev"
)

type Options struct {
	GitExecutable        string
	CodexExecutable      string
	SupervisorCommand    []string
	HTTPClient           *http.Client
	Random               io.Reader
	WorkerVersion        string
	PollInterval         time.Duration
	HealthInterval       time.Duration
	RegistrationInterval time.Duration
	LeaseRenewInterval   time.Duration
	LeaseRetryInterval   time.Duration
	TransportBackoffMin  time.Duration
	TransportBackoffMax  time.Duration
	ShutdownTimeout      time.Duration
}

type Manager struct {
	config            Config
	options           Options
	logger            *slog.Logger
	id                string
	dataDirectory     string
	lock              *dataLock
	repositories      []Repository
	repositoriesByKey map[string]Repository
	client            *client
	manifests         *manifestStore
	slots             chan struct{}

	stateMutex     sync.Mutex
	health         health
	active         map[string]*attemptHandle
	seen           map[string]bool
	retained       map[string]protocol.RetainedWorktree
	retainedCounts map[string]int
	disposed       map[string]bool
	pending        map[string]context.CancelFunc
	fatalHealth    error
	registered     bool
	closed         bool

	randomMutex sync.Mutex
	// registrationMutex keeps a periodic registration from overtaking the
	// terminal-attempt to retained-worktree capacity handoff.
	registrationMutex sync.Mutex
	waitGroup         sync.WaitGroup
}

type attemptHandle struct {
	context context.Context
	cancel  context.CancelFunc
	done    chan struct{}

	mutex         sync.Mutex
	reason        string
	expiry        time.Time
	supervisor    *supervisorProcess
	manifestReady bool
}

func New(config Config, options Options, logger *slog.Logger) (*Manager, error) {
	if err := ensureSupportedPlatform(); err != nil {
		return nil, err
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	options = options.withDefaults()
	if len(options.SupervisorCommand) == 0 {
		return nil, errors.New("resolve factory-worker executable for attempt supervision")
	}
	dataDirectory, err := resolveDataDirectory(config.DataDirectory)
	if err != nil {
		return nil, err
	}
	lock, err := lockDataDirectory(dataDirectory)
	if err != nil {
		return nil, err
	}
	cleanupLock := true
	defer func() {
		if cleanupLock {
			_ = lock.Close()
		}
	}()
	id, err := loadOrCreateWorkerID(dataDirectory, options.Random)
	if err != nil {
		return nil, err
	}
	repositories, err := resolveRepositories(config, options.GitExecutable)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	byKey := make(map[string]Repository, len(repositories))
	for _, repository := range repositories {
		byKey[repository.Key] = repository
	}
	cleanupLock = false
	return &Manager{
		config:            config,
		options:           options,
		logger:            logger,
		id:                id,
		dataDirectory:     dataDirectory,
		lock:              lock,
		repositories:      repositories,
		repositoriesByKey: byKey,
		client:            newClient(config.Server, options.HTTPClient),
		manifests:         newManifestStore(dataDirectory, id),
		slots:             make(chan struct{}, config.MaxConcurrent),
		health:            health{State: "unhealthy"},
		active:            make(map[string]*attemptHandle),
		seen:              make(map[string]bool),
		retained:          make(map[string]protocol.RetainedWorktree),
		retainedCounts:    make(map[string]int),
		disposed:          make(map[string]bool),
		pending:           make(map[string]context.CancelFunc),
	}, nil
}

func (options Options) withDefaults() Options {
	if options.GitExecutable == "" {
		options.GitExecutable = "git"
	}
	if options.CodexExecutable == "" {
		options.CodexExecutable = "codex"
	}
	if len(options.SupervisorCommand) == 0 {
		executable, err := os.Executable()
		if err == nil {
			options.SupervisorCommand = supervisorCommandLine(executable)
		}
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.WorkerVersion == "" {
		options.WorkerVersion = defaultWorkerVersion
	}
	if options.PollInterval <= 0 {
		options.PollInterval = defaultPollInterval
	}
	if options.HealthInterval <= 0 {
		options.HealthInterval = defaultHealthInterval
	}
	if options.RegistrationInterval <= 0 {
		options.RegistrationInterval = defaultRegistrationInterval
	}
	if options.LeaseRenewInterval <= 0 {
		options.LeaseRenewInterval = defaultLeaseRenewInterval
	}
	if options.LeaseRetryInterval <= 0 {
		options.LeaseRetryInterval = defaultLeaseRetryInterval
	}
	if options.TransportBackoffMin <= 0 {
		options.TransportBackoffMin = time.Second
	}
	if options.TransportBackoffMax <= 0 {
		options.TransportBackoffMax = 30 * time.Second
	}
	if options.ShutdownTimeout <= 0 {
		options.ShutdownTimeout = defaultShutdownTimeout
	}
	return options
}

func (manager *Manager) ID() string { return manager.id }

func (manager *Manager) Run(ctx context.Context) error {
	defer manager.Close()
	for {
		err := manager.reconcile(ctx)
		if err == nil {
			break
		}
		if !reconciliationNeedsRetry(err) {
			manager.markUnhealthy("startup_reconciliation", err)
			break
		}
		manager.logger.Warn("startup_reconciliation_retry", "error_class", "transient", "error", err)
		timer := time.NewTimer(manager.options.LeaseRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	manager.setHealth(checkHealth(ctx, manager.options.GitExecutable, manager.options.CodexExecutable))
	manager.register(ctx)

	healthTicker := time.NewTicker(manager.options.HealthInterval)
	defer healthTicker.Stop()
	registrationTicker := time.NewTicker(manager.options.RegistrationInterval)
	defer registrationTicker.Stop()
	claimTimer := time.NewTimer(0)
	defer claimTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			manager.stopAll("cancelled")
			return manager.waitForShutdown()
		case <-healthTicker.C:
			manager.setHealth(checkHealth(ctx, manager.options.GitExecutable, manager.options.CodexExecutable))
		case <-registrationTicker.C:
			manager.register(ctx)
		case <-claimTimer.C:
			if manager.isHealthy() {
				manager.reserveAndClaim(ctx)
			}
			claimTimer.Reset(manager.jitteredPollInterval())
		}
	}
}

func (manager *Manager) Close() error {
	manager.stateMutex.Lock()
	if manager.closed {
		manager.stateMutex.Unlock()
		return nil
	}
	manager.closed = true
	manager.stateMutex.Unlock()
	return manager.lock.Close()
}

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
		manager.logger.Info("worker_healthy", "git_version", value.GitVersion, "codex_version", value.CodexVersion)
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
	for _, value := range manager.retained {
		retained = append(retained, value)
	}
	for attemptID := range manager.disposed {
		disposedAttemptIDs = append(disposedAttemptIDs, attemptID)
	}
	for _, repository := range manager.repositories {
		repositories = append(repositories, protocol.RepositoryRegistration{
			Key: repository.Key, RemoteIdentity: repository.RemoteIdentity,
			RetainedCount: manager.retainedCounts[repository.RemoteIdentity],
		})
	}
	return protocol.WorkerRegistration{
		Name:                   strings.TrimSpace(manager.config.Name),
		WorkerVersion:          manager.options.WorkerVersion,
		CodexVersion:           manager.health.CodexVersion,
		Capacity:               manager.config.MaxConcurrent,
		ActiveCount:            len(manager.slots),
		Health:                 manager.health.State,
		Repositories:           repositories,
		RetainedWorktrees:      retained,
		CapacityHandoffVersion: 1,
		DisposedAttemptIDs:     disposedAttemptIDs,
	}
}

func (manager *Manager) register(ctx context.Context) {
	manager.registrationMutex.Lock()
	defer manager.registrationMutex.Unlock()
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

func (manager *Manager) reserveAndClaim(ctx context.Context) {
	for {
		select {
		case manager.slots <- struct{}{}:
			manager.waitGroup.Add(1)
			go manager.claimOnce(ctx)
		default:
			return
		}
	}
}

func (manager *Manager) claimOnce(ctx context.Context) {
	defer manager.waitGroup.Done()
	release := true
	defer func() {
		if release {
			<-manager.slots
		}
	}()
	requestID, err := manager.randomUUID()
	if err != nil {
		manager.markUnhealthy("randomness", err)
		return
	}
	token, err := manager.randomSecret()
	if err != nil {
		manager.markUnhealthy("randomness", err)
		return
	}
	claimContext, cancelClaim, eligible := manager.beginClaim(ctx, requestID)
	if !eligible {
		cancelClaim()
		return
	}
	claim, err := manager.client.claim(claimContext, manager.id, protocol.ClaimRequest{
		RequestID: requestID, LeaseToken: token,
	}, manager.options.TransportBackoffMin, manager.options.TransportBackoffMax)
	eligible = manager.endClaim(requestID)
	cancelClaim()
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			manager.logger.Warn("worker_claim_failed", "error_class", apiErrorClass(err))
		}
		return
	}
	if claim == nil {
		return
	}
	if !eligible {
		handle := &attemptHandle{expiry: claim.Attempt.LeaseExpiresAt}
		manager.finishWithoutWorktree(*claim, token, handle, "failed",
			errors.New("worker became ineligible before attempt start"))
		return
	}
	manager.stateMutex.Lock()
	if manager.seen[claim.Attempt.ID] {
		manager.stateMutex.Unlock()
		manager.logger.Warn("duplicate_claim_ignored", "attempt_id", claim.Attempt.ID)
		return
	}
	manager.seen[claim.Attempt.ID] = true
	manager.stateMutex.Unlock()
	release = false
	manager.runAttempt(ctx, *claim, token)
	<-manager.slots
}

func (manager *Manager) beginClaim(
	parent context.Context,
	requestID string,
) (context.Context, context.CancelFunc, bool) {
	ctx, cancel := context.WithCancel(parent)
	manager.stateMutex.Lock()
	defer manager.stateMutex.Unlock()
	if manager.health.State != "healthy" || !manager.registered {
		return ctx, cancel, false
	}
	manager.pending[requestID] = cancel
	return ctx, cancel, true
}

func (manager *Manager) endClaim(requestID string) bool {
	manager.stateMutex.Lock()
	defer manager.stateMutex.Unlock()
	delete(manager.pending, requestID)
	return manager.health.State == "healthy" && manager.registered
}

func (manager *Manager) cancelPendingClaimsLocked() {
	for requestID, cancel := range manager.pending {
		cancel()
		delete(manager.pending, requestID)
	}
}

func (manager *Manager) runAttempt(parent context.Context, claim protocol.Claim, token string) {
	attemptContext, cancel := context.WithCancel(parent)
	handle := &attemptHandle{
		context: attemptContext, cancel: cancel, done: make(chan struct{}), expiry: claim.Attempt.LeaseExpiresAt,
	}
	manager.stateMutex.Lock()
	manager.active[claim.Attempt.ID] = handle
	manager.stateMutex.Unlock()
	if parent.Err() != nil {
		handle.stop("cancelled")
	}
	defer func() {
		close(handle.done)
		cancel()
		manager.stateMutex.Lock()
		delete(manager.active, claim.Attempt.ID)
		manager.stateMutex.Unlock()
	}()
	go manager.heartbeatAttempt(handle, claim.Attempt.ID, token)

	repository, err := manager.validateClaim(claim)
	if err != nil {
		manager.finishWithoutWorktree(claim, token, handle, "failed", err)
		return
	}
	taskDeadline := claim.Attempt.CreatedAt.Add(time.Duration(claim.Task.TimeoutSeconds) * time.Second)
	taskTimer := time.AfterFunc(time.Until(taskDeadline), func() {
		handle.stop("timeout")
	})
	defer taskTimer.Stop()
	worktreeRoot := filepath.Join(manager.dataDirectory, "worktrees")
	value, err := prepareWorktree(handle.context, manager.options.GitExecutable, worktreeRoot,
		repository, claim.Task.ID, claim.Attempt.ID)
	if err != nil {
		manager.finishWithoutWorktree(claim, token, handle, terminalForStop(handle), stoppedAttemptError(handle, err))
		return
	}
	manifest := attemptManifest{
		TaskID: claim.Task.ID, ExecutionID: claim.Execution.ID, AttemptID: claim.Attempt.ID,
		RepositoryID: claim.Repository.ID, RepositoryKey: repository.Key,
		RepositoryPath: repository.Path, RemoteIdentity: repository.RemoteIdentity,
		BaseCommit: value.BaseCommit, WorktreePath: value.Path, Branch: value.Branch,
		LeaseDeadline: claim.Attempt.LeaseExpiresAt, Lifecycle: manifestPreparing,
	}
	if err := manager.manifests.create(manifest); err != nil {
		manager.markUnhealthy("manifest_write", err)
		manager.finishWithoutWorktree(claim, token, handle, "failed", err)
		return
	}
	handle.setManifestReady()
	if err := addPreparedWorktree(handle.context, manager.options.GitExecutable, repository, value); err != nil {
		state := "failed"
		if handle.stopReason() == "cancelled" {
			state = "cancelled"
		}
		err = stoppedAttemptError(handle, err)
		persisted, loadErr := manager.manifests.load(claim.Attempt.ID)
		inspection, inspectErr := inspectManifestWorktree(
			context.Background(), manager.options.GitExecutable, manager.dataDirectory, persisted)
		if loadErr != nil || inspectErr != nil {
			identityErr := errors.Join(loadErr, inspectErr)
			_ = manager.persistLifecycle(claim.Attempt.ID, manifestInconsistent, func(manifest *attemptManifest) {
				manifest.RetentionReason = boundedText(identityErr.Error(), 1000)
			})
			manager.markUnhealthy("worktree_identity", identityErr)
			manager.complete(claim.Attempt.ID, token, state, "", err.Error(), handle)
			return
		}
		if inspection.PathExists && inspection.Registered {
			manager.finishWithWorktree(claim, token, handle, repository, value, state, "", err.Error())
			return
		}
		if inspection.PathExists || inspection.Registered {
			identityErr := errors.New("worktree creation left partial filesystem or Git registration state")
			_ = manager.persistLifecycle(claim.Attempt.ID, manifestInconsistent, func(manifest *attemptManifest) {
				manifest.RetentionReason = identityErr.Error()
			})
			manager.markUnhealthy("worktree_identity", identityErr)
			manager.complete(claim.Attempt.ID, token, state, "", err.Error(), handle)
			return
		}
		manager.finishWithoutWorktree(claim, token, handle, state, err)
		return
	}
	if err := manager.persistLifecycle(claim.Attempt.ID, manifestWorktreeCreated, nil); err != nil {
		manager.markUnhealthy("manifest_write", err)
		manager.finishWithWorktree(claim, token, handle, repository, value, "failed", "", err.Error())
		return
	}

	path, err := resultPath(manager.dataDirectory, claim.Attempt.ID)
	if err != nil {
		manager.finishWithWorktree(claim, token, handle, repository, value, "failed", "", err.Error())
		return
	}
	defer os.Remove(path)
	process, err := startSupervisor(manager.options.SupervisorCommand, supervisorInit{
		CodexExecutable: manager.options.CodexExecutable,
		Worktree:        value.Path,
		ResultPath:      path,
		Prompt:          buildPrompt(claim),
		TimeoutSeconds:  remainingTimeoutSeconds(taskDeadline),
	}, os.Stderr)
	if err != nil {
		manager.finishWithWorktree(claim, token, handle, repository, value, "failed", "", err.Error())
		return
	}
	if err := process.awaitReady(handle.context); err != nil {
		_ = process.closeControl()
		manager.finishWithWorktree(claim, token, handle, repository, value,
			terminalForStop(handle), "", stoppedAttemptError(handle, err).Error())
		return
	}
	if _, err := manager.manifests.update(claim.Attempt.ID, func(manifest *attemptManifest) error {
		manifest.SupervisorPID = process.supervisorPID
		manifest.SupervisorIdentity = process.supervisorIdentity
		manifest.ProcessGroupID = process.processGroupID
		manifest.ProcessGroupIdentity = process.groupIdentity
		manifest.ProcessActive = true
		manifest.LeaseDeadline = handle.leaseExpiry()
		manifest.Lifecycle = manifestSupervisorReady
		return nil
	}); err != nil {
		manager.emergencyStop(process, err)
		manager.markUnhealthy("manifest_write", err)
		manager.finishWithWorktree(claim, token, handle, repository, value, "failed", "", err.Error())
		return
	}
	handle.setSupervisor(process)

	started, err := manager.client.start(handle.context, claim.Attempt.ID, supervisorStartRequest(process, token))
	if err != nil {
		reason := handle.stopReason()
		var apiError *APIError
		if errors.As(err, &apiError) && apiError.Code == "lease_not_owner" {
			reason = "lease_lost"
		}
		if reason == "" {
			reason = "failed"
		}
		handle.stop(reason)
		message := manager.waitForSupervisor(process)
		errorText := err.Error()
		if reason == "timeout" {
			errorText = "task timeout reached"
		}
		manager.finishWithWorktree(claim, token, handle, repository, value,
			terminalState(message), message.Result, firstNonEmpty(errorText, message.Error))
		return
	}
	if started.State != "running" {
		handle.stop("lease_lost")
		message := manager.waitForSupervisor(process)
		manager.finishWithWorktree(claim, token, handle, repository, value, "failed", message.Result,
			"control plane did not accept the running transition")
		return
	}
	if err := manager.persistLifecycle(claim.Attempt.ID, manifestRunning, nil); err != nil {
		handle.stop("failed")
		manager.emergencyStop(process, err)
		manager.markUnhealthy("manifest_write", err)
		manager.finishWithWorktree(claim, token, handle, repository, value, "failed", "", err.Error())
		return
	}
	if err := process.send("start"); err != nil {
		manager.emergencyStop(process, err)
		manager.finishWithWorktree(claim, token, handle, repository, value, "failed", "", err.Error())
		return
	}
	manager.logger.Info("attempt_started", "attempt_id", claim.Attempt.ID, "repository", repository.Key,
		"process", processSummary(process))
	sender := newEventSender(handle.context, manager.client, claim.Attempt.ID, token)
	message := manager.waitForSupervisorWithEvents(process, sender)
	sender.closeAndWait(5 * time.Second)
	manager.finishWithWorktree(claim, token, handle, repository, value,
		terminalState(message), message.Result, message.Error)
}

func (manager *Manager) validateClaim(claim protocol.Claim) (Repository, error) {
	if !uuidPattern.MatchString(claim.Attempt.ID) || !uuidPattern.MatchString(claim.Task.ID) {
		return Repository{}, errors.New("claim contains invalid IDs")
	}
	if claim.Attempt.WorkerID != manager.id || claim.Execution.AssignedWorkerID != manager.id {
		return Repository{}, errors.New("claim is assigned to a different worker")
	}
	if claim.Execution.RequiredRuntime != "codex" {
		return Repository{}, errors.New("claim requires an unsupported runtime")
	}
	if claim.Task.RepositoryID != claim.Repository.ID {
		return Repository{}, errors.New("claim repository IDs do not match")
	}
	if claim.Task.TimeoutSeconds < 1 || claim.Task.TimeoutSeconds > int(protocol.MaxTimeout/time.Second) {
		return Repository{}, errors.New("claim timeout is outside the supported range")
	}
	repository, exists := manager.repositoriesByKey[claim.Repository.Key]
	if !exists || repository.RemoteIdentity != claim.Repository.RemoteIdentity {
		return Repository{}, errors.New("claim repository is not advertised by this worker")
	}
	return repository, nil
}

func (manager *Manager) heartbeatAttempt(handle *attemptHandle, attemptID, token string) {
	delay := manager.options.LeaseRenewInterval
	for {
		timer := time.NewTimer(delay)
		select {
		case <-handle.done:
			timer.Stop()
			return
		case <-timer.C:
		}
		requestContext, cancel := context.WithTimeout(context.Background(), requestTimeout)
		heartbeat, err := manager.client.heartbeat(requestContext, attemptID, token)
		cancel()
		if err == nil {
			if handle.hasManifest() {
				if _, persistErr := manager.manifests.update(attemptID, func(manifest *attemptManifest) error {
					manifest.LeaseDeadline = heartbeat.LeaseExpiresAt
					return nil
				}); persistErr != nil {
					manager.markUnhealthy("manifest_write", persistErr)
					handle.stop("failed")
					return
				}
			}
			handle.updateExpiry(heartbeat.LeaseExpiresAt)
			if heartbeat.CancellationRequested {
				handle.stop("cancelled")
				return
			}
			delay = manager.options.LeaseRenewInterval
			continue
		}
		var apiError *APIError
		if errors.As(err, &apiError) && apiError.Code == "lease_not_owner" {
			handle.stop("lease_lost")
			return
		}
		if time.Now().After(handle.leaseExpiry()) {
			handle.stop("lease_lost")
			return
		}
		delay = manager.options.LeaseRetryInterval
	}
}

func (handle *attemptHandle) setSupervisor(process *supervisorProcess) {
	handle.mutex.Lock()
	handle.supervisor = process
	reason := handle.reason
	expiry := handle.expiry
	handle.mutex.Unlock()
	if reason != "" {
		_ = process.send(stopCommand(reason))
	} else {
		_ = process.renew(expiry)
	}
}

func (handle *attemptHandle) setManifestReady() {
	handle.mutex.Lock()
	handle.manifestReady = true
	handle.mutex.Unlock()
}

func (handle *attemptHandle) hasManifest() bool {
	handle.mutex.Lock()
	defer handle.mutex.Unlock()
	return handle.manifestReady
}

func (handle *attemptHandle) updateExpiry(expiry time.Time) {
	handle.mutex.Lock()
	handle.expiry = expiry
	process := handle.supervisor
	handle.mutex.Unlock()
	if process != nil {
		_ = process.renew(expiry)
	}
}

func (handle *attemptHandle) leaseExpiry() time.Time {
	handle.mutex.Lock()
	defer handle.mutex.Unlock()
	return handle.expiry
}

func (handle *attemptHandle) stop(reason string) {
	handle.mutex.Lock()
	if handle.reason != "" {
		handle.mutex.Unlock()
		return
	}
	handle.reason = reason
	process := handle.supervisor
	handle.mutex.Unlock()
	handle.cancel()
	if process != nil {
		_ = process.send(stopCommand(reason))
	}
}

func (handle *attemptHandle) stopReason() string {
	handle.mutex.Lock()
	defer handle.mutex.Unlock()
	return handle.reason
}

func (manager *Manager) waitForSupervisorWithEvents(process *supervisorProcess, sender *eventSender) supervisorMessage {
	for {
		message := manager.waitForSupervisorMessage(process)
		if message.Type == "output" {
			sender.enqueue(message.Stream, message.Text, message.Truncated)
			continue
		}
		return message
	}
}

func (manager *Manager) waitForSupervisor(process *supervisorProcess) supervisorMessage {
	for {
		message := manager.waitForSupervisorMessage(process)
		if message.Type != "output" {
			return message
		}
	}
}

func (manager *Manager) waitForSupervisorMessage(process *supervisorProcess) supervisorMessage {
	for {
		select {
		case message := <-process.messages:
			if message.Type == "exit" || message.Type == "output" {
				if message.Type == "exit" {
					_ = process.closeControl()
					process.markStopped()
				}
				return message
			}
		case err := <-process.decodeErrors:
			select {
			case message := <-process.messages:
				if message.Type == "exit" || message.Type == "output" {
					if message.Type == "exit" {
						process.markStopped()
					}
					return message
				}
			default:
			}
			manager.emergencyStop(process, err)
			manager.markUnhealthy("attempt_supervisor", err)
			return supervisorMessage{Type: "exit", Reason: "supervisor_error", ExitCode: -1,
				Error: "attempt supervisor output ended unexpectedly"}
		}
	}
}

func (manager *Manager) emergencyStop(process *supervisorProcess, cause error) {
	_ = process.closeControl()
	if process.processGroupID > 0 && process.groupIdentity != "" {
		if err := stopOwnedProcessGroup(int(process.processGroupID), process.groupIdentity, terminationGrace); err != nil {
			manager.markUnhealthy("process_group_stop", errors.Join(cause, err))
			return
		}
		process.markStopped()
	}
}

func (manager *Manager) finishWithoutWorktree(
	claim protocol.Claim,
	token string,
	handle *attemptHandle,
	state string,
	cause error,
) {
	manager.registrationMutex.Lock()
	defer manager.registrationMutex.Unlock()
	defer func() {
		registerContext, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		manager.registerLocked(registerContext)
	}()

	errorText := ""
	if cause != nil {
		errorText = boundedText(cause.Error(), protocol.MaxErrorBytes)
	}
	if err := manager.recordDisposed(claim.Attempt.ID); err != nil {
		manager.markUnhealthy("disposal_journal", err)
		return
	}
	if _, err := manager.manifests.load(claim.Attempt.ID); err == nil {
		if persistErr := manager.persistLifecycle(claim.Attempt.ID, manifestNotCreated, func(manifest *attemptManifest) {
			manifest.TerminalState = state
			manifest.RetentionReason = ""
			manifest.ProcessActive = false
		}); persistErr != nil {
			manager.markUnhealthy("manifest_write", persistErr)
		}
	}
	manager.complete(claim.Attempt.ID, token, state, "", errorText, handle)
}

func (manager *Manager) finishWithWorktree(
	claim protocol.Claim,
	token string,
	handle *attemptHandle,
	repository Repository,
	value worktree,
	state string,
	result string,
	errorText string,
) {
	manager.registrationMutex.Lock()
	defer manager.registrationMutex.Unlock()
	defer func() {
		registerContext, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		manager.registerLocked(registerContext)
	}()

	result = boundedText(result, protocol.MaxResultBytes)
	errorText = boundedText(errorText, protocol.MaxErrorBytes)
	if err := manager.persistLifecycle(claim.Attempt.ID, manifestCompleted, func(manifest *attemptManifest) {
		manifest.TerminalState = state
		manifest.ProcessActive = handle.processStillActive()
	}); err != nil {
		manager.markUnhealthy("manifest_write", err)
		errorText = firstNonEmpty(errorText, err.Error())
	}
	completed := manager.complete(claim.Attempt.ID, token, state, result, errorText, handle)
	if completed && state == "succeeded" {
		err := manager.cleanCompletedWorktree(claim.Attempt.ID)
		if err == nil {
			if err := manager.recordDisposed(claim.Attempt.ID); err != nil {
				manager.markUnhealthy("disposal_journal", err)
			}
			manager.logger.Info("attempt_worktree_cleaned", "attempt_id", claim.Attempt.ID, "repository", repository.Key)
			return
		}
		if manifest, loadErr := manager.manifests.load(claim.Attempt.ID); loadErr == nil &&
			manifest.Lifecycle == manifestCleanupStarted {
			manager.markUnhealthy("worktree_cleanup", err)
			return
		}
		errorText = err.Error()
	}
	reason := firstNonEmpty(errorText, state+" attempt retained for inspection")
	manager.retain(claim, repository, value, reason)
}

func (handle *attemptHandle) processStillActive() bool {
	handle.mutex.Lock()
	process := handle.supervisor
	handle.mutex.Unlock()
	return process != nil && !process.isStopped()
}

func (manager *Manager) complete(
	attemptID string,
	token string,
	state string,
	result string,
	errorText string,
	handle *attemptHandle,
) bool {
	timeout := requestTimeout
	if remaining := time.Until(handle.leaseExpiry()); remaining > 0 && remaining < timeout {
		timeout = remaining
	}
	if timeout <= 0 {
		manager.logger.Warn("attempt_completion_not_recorded", "attempt_id", attemptID, "error_class", "lease_expired")
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, err := manager.client.complete(ctx, attemptID, protocol.CompleteAttemptRequest{
		LeaseToken: token, State: state, Result: result, Error: errorText,
	})
	if err != nil {
		manager.logger.Warn("attempt_completion_not_recorded", "attempt_id", attemptID, "error_class", apiErrorClass(err))
		return false
	}
	manager.logger.Info("attempt_completed", "attempt_id", attemptID, "state", state)
	return true
}

func (manager *Manager) retain(claim protocol.Claim, repository Repository, value worktree, reason string) {
	manifest, err := manager.manifests.load(claim.Attempt.ID)
	if err != nil {
		manager.markUnhealthy("manifest_read", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*gitCommandTimeout)
	inspection, inspectErr := inspectManifestWorktree(ctx, manager.options.GitExecutable, manager.dataDirectory, manifest)
	cancel()
	if inspectErr != nil || !inspection.PathExists || !inspection.Registered {
		if inspectErr == nil {
			inspectErr = errors.New("worktree exists in only one of the filesystem and Git worktree registry")
		}
		_ = manager.persistLifecycle(claim.Attempt.ID, manifestInconsistent, func(value *attemptManifest) {
			value.RetentionReason = boundedText(inspectErr.Error(), 1000)
		})
		manager.markUnhealthy("worktree_identity", inspectErr)
		return
	}
	updated, err := manager.manifests.update(claim.Attempt.ID, func(manifest *attemptManifest) error {
		manifest.Lifecycle = manifestRetained
		manifest.RetentionReason = boundedText(reason, 1000)
		return nil
	})
	if err != nil {
		manager.markUnhealthy("manifest_write", err)
		return
	}
	manager.recordRetained(updated)
	manager.logger.Info("attempt_worktree_retained", "attempt_id", claim.Attempt.ID, "repository", repository.Key)
}

func (manager *Manager) stopAll(reason string) {
	manager.stateMutex.Lock()
	handles := make([]*attemptHandle, 0, len(manager.active))
	for _, handle := range manager.active {
		handles = append(handles, handle)
	}
	manager.stateMutex.Unlock()
	for _, handle := range handles {
		handle.stop(reason)
	}
}

func (manager *Manager) waitForShutdown() error {
	done := make(chan struct{})
	go func() {
		manager.waitGroup.Wait()
		close(done)
	}()
	timer := time.NewTimer(manager.options.ShutdownTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		manager.stateMutex.Lock()
		for _, handle := range manager.active {
			handle.mutex.Lock()
			if handle.supervisor != nil {
				_ = handle.supervisor.closeControl()
			}
			handle.mutex.Unlock()
		}
		manager.stateMutex.Unlock()
		return errors.New("worker shutdown exceeded thirty seconds")
	}
}

func (manager *Manager) randomUUID() (string, error) {
	manager.randomMutex.Lock()
	defer manager.randomMutex.Unlock()
	return newUUID(manager.options.Random)
}

func (manager *Manager) randomSecret() (string, error) {
	manager.randomMutex.Lock()
	defer manager.randomMutex.Unlock()
	body := make([]byte, 32)
	if _, err := io.ReadFull(manager.options.Random, body); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

func (manager *Manager) jitteredPollInterval() time.Duration {
	manager.randomMutex.Lock()
	defer manager.randomMutex.Unlock()
	var value [1]byte
	if _, err := io.ReadFull(manager.options.Random, value[:]); err != nil {
		return manager.options.PollInterval
	}
	// Empty polls use up to 20 percent positive jitter.
	return manager.options.PollInterval + time.Duration(int64(manager.options.PollInterval)*int64(value[0])/1275)
}

func terminalForStop(handle *attemptHandle) string {
	if handle.stopReason() == "cancelled" {
		return "cancelled"
	}
	return "failed"
}

func stoppedAttemptError(handle *attemptHandle, fallback error) error {
	switch handle.stopReason() {
	case "cancelled":
		return errors.New("attempt cancelled")
	case "timeout":
		return errors.New("task timeout reached")
	case "lease_lost":
		return errors.New("control-plane lease was lost")
	default:
		return fallback
	}
}

func terminalState(message supervisorMessage) string {
	if message.Reason == "cancelled" {
		return "cancelled"
	}
	if message.Reason == "exited" && message.ExitCode == 0 {
		return "succeeded"
	}
	return "failed"
}

func buildPrompt(claim protocol.Claim) string {
	return "You are running in a Factory V2 managed Git worktree.\n" +
		"Work only on the assigned task and repository. Preserve unrelated changes, do not touch Factory V1 state or worktrees, " +
		"and do not delete worktrees or branches. Complete and verify the task before returning a concise result.\n\n" +
		"Task title: " + claim.Task.Title + "\n" +
		"Repository: " + claim.Repository.RemoteIdentity + "\n\n" +
		claim.Task.Description
}

func apiErrorClass(err error) string {
	var apiError *APIError
	if errors.As(err, &apiError) && apiError.Code != "" {
		return apiError.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "transport"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stopCommand(reason string) string {
	if reason == "cancelled" {
		return "cancel"
	}
	if reason == "failed" {
		return "fail"
	}
	return reason
}

func remainingTimeoutSeconds(deadline time.Time) int {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 1
	}
	seconds := int((remaining + time.Second - 1) / time.Second)
	maximum := int(protocol.MaxTimeout / time.Second)
	if seconds > maximum {
		return maximum
	}
	return seconds
}

func DefaultConfigPath() (string, error) {
	if value := os.Getenv("FACTORY_V2_WORKER_CONFIG"); value != "" {
		return value, nil
	}
	root := os.Getenv("FACTORY_V2_DATA_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		root = filepath.Join(home, ".factory-v2")
	}
	return filepath.Join(root, "worker.toml"), nil
}

package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

const (
	supervisorCommand       = "__factory_worker_supervisor"
	supervisorReadyTimeout  = 10 * time.Second
	terminationGrace        = 5 * time.Second
	maxSupervisorLineBytes  = 1 << 20
	maxSupervisorErrorBytes = 64 << 10
)

type supervisorInit struct {
	CodexExecutable string `json:"codex_executable"`
	Worktree        string `json:"worktree"`
	ResultPath      string `json:"result_path"`
	Prompt          string `json:"prompt"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
}

type supervisorMessage struct {
	Type            string `json:"type"`
	SupervisorPID   int64  `json:"supervisor_pid,omitempty"`
	ProcessIdentity string `json:"process_identity,omitempty"`
	ProcessGroupID  int64  `json:"process_group_id,omitempty"`
	GroupIdentity   string `json:"group_identity,omitempty"`
	Stream          string `json:"stream,omitempty"`
	Text            string `json:"text,omitempty"`
	ExitCode        int    `json:"exit_code,omitempty"`
	Reason          string `json:"reason,omitempty"`
	Result          string `json:"result,omitempty"`
	Error           string `json:"error,omitempty"`
	Truncated       bool   `json:"truncated,omitempty"`
}

type supervisorProcess struct {
	command            *exec.Cmd
	control            *os.File
	messages           chan supervisorMessage
	decodeErrors       chan error
	wait               chan error
	stderr             *limitBuffer
	controlMutex       sync.Mutex
	supervisorPID      int64
	supervisorIdentity string
	processGroupID     int64
	groupIdentity      string
}

func IsSupervisorCommand(arguments []string) bool {
	return len(arguments) > 0 && arguments[0] == supervisorCommand
}

func RunSupervisor(control *os.File, input io.Reader, output, errorOutput io.Writer) error {
	if err := ensureSupportedPlatform(); err != nil {
		return err
	}
	if control == nil {
		return errors.New("supervisor control pipe is required")
	}
	var init supervisorInit
	decoder := json.NewDecoder(io.LimitReader(input, protocol.MaxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&init); err != nil {
		return fmt.Errorf("decode supervisor input: %w", err)
	}
	if init.CodexExecutable == "" || init.Worktree == "" || init.ResultPath == "" || init.TimeoutSeconds < 1 {
		return errors.New("supervisor input is incomplete")
	}
	if init.TimeoutSeconds > int(protocol.MaxTimeout/time.Second) {
		return errors.New("supervisor timeout exceeds eight hours")
	}

	anchorToken, err := newUUID(nil)
	if err != nil {
		return fmt.Errorf("create process-group token: %w", err)
	}
	anchor := exec.Command("/bin/sh", "-c", "trap '' TERM; while :; do sleep 3600; done", "factory-v2-anchor-"+anchorToken)
	anchor.Stdin = nil
	anchor.Stdout = nil
	anchor.Stderr = errorOutput
	configureNewProcessGroup(anchor)
	if err := anchor.Start(); err != nil {
		return fmt.Errorf("start process-group anchor: %w", err)
	}
	anchorPID := anchor.Process.Pid
	anchorIdentity, err := processIdentity(anchorPID)
	if err != nil {
		_ = anchor.Process.Kill()
		_ = anchor.Wait()
		return fmt.Errorf("establish process-group identity: %w", err)
	}
	groupID, err := processGroupID(anchorPID)
	if err != nil || groupID != anchorPID {
		_ = anchor.Process.Kill()
		_ = anchor.Wait()
		return errors.New("process-group anchor did not become its group leader")
	}
	supervisorIdentity, err := processIdentity(os.Getpid())
	if err != nil {
		_ = stopOwnedProcessGroup(anchorPID, anchorIdentity, 0)
		_ = anchor.Wait()
		return fmt.Errorf("establish supervisor process identity: %w", err)
	}

	writer := &synchronizedEncoder{encoder: json.NewEncoder(output)}
	if err := writer.send(supervisorMessage{
		Type:            "ready",
		SupervisorPID:   int64(os.Getpid()),
		ProcessIdentity: supervisorIdentity,
		ProcessGroupID:  int64(groupID),
		GroupIdentity:   anchorIdentity,
	}); err != nil {
		_ = stopOwnedProcessGroup(anchorPID, anchorIdentity, 0)
		_ = anchor.Wait()
		return fmt.Errorf("report supervisor readiness: %w", err)
	}

	commands := make(chan string, 8)
	controlErrors := make(chan error, 1)
	go readSupervisorControl(control, commands, controlErrors)
	leaseTimer := time.NewTimer(leaseStopDelay(protocol.LeaseDuration))
	defer leaseTimer.Stop()

	for {
		select {
		case command := <-commands:
			action, duration, parseErr := parseControlCommand(command)
			if parseErr != nil {
				_ = stopOwnedProcessGroup(anchorPID, anchorIdentity, 0)
				_ = anchor.Wait()
				return parseErr
			}
			switch action {
			case "renew":
				resetTimer(leaseTimer, leaseStopDelay(duration))
			case "cancel":
				_ = stopOwnedProcessGroup(anchorPID, anchorIdentity, terminationGrace)
				_ = anchor.Wait()
				return writer.send(supervisorMessage{Type: "exit", Reason: "cancelled", Error: "attempt cancelled"})
			case "lease_lost":
				_ = stopOwnedProcessGroup(anchorPID, anchorIdentity, terminationGrace)
				_ = anchor.Wait()
				return writer.send(supervisorMessage{Type: "exit", Reason: "lease_lost", Error: "control-plane lease was lost"})
			case "fail":
				_ = stopOwnedProcessGroup(anchorPID, anchorIdentity, terminationGrace)
				_ = anchor.Wait()
				return writer.send(supervisorMessage{Type: "exit", Reason: "supervisor_error", Error: "attempt preparation failed"})
			case "timeout":
				_ = stopOwnedProcessGroup(anchorPID, anchorIdentity, terminationGrace)
				_ = anchor.Wait()
				return writer.send(supervisorMessage{Type: "exit", Reason: "timeout", Error: "task timeout reached"})
			case "start":
				return superviseCodex(init, anchor, anchorIdentity, groupID, commands, controlErrors, leaseTimer, writer)
			}
		case err := <-controlErrors:
			_ = stopOwnedProcessGroup(anchorPID, anchorIdentity, terminationGrace)
			_ = anchor.Wait()
			if err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("read supervisor control pipe: %w", err)
			}
			return nil
		case <-leaseTimer.C:
			_ = stopOwnedProcessGroup(anchorPID, anchorIdentity, terminationGrace)
			_ = anchor.Wait()
			return writer.send(supervisorMessage{Type: "exit", Reason: "lease_lost", Error: "control-plane lease renewal deadline passed"})
		}
	}
}

func superviseCodex(
	init supervisorInit,
	anchor *exec.Cmd,
	anchorIdentity string,
	groupID int,
	commands <-chan string,
	controlErrors <-chan error,
	leaseTimer *time.Timer,
	writer *synchronizedEncoder,
) error {
	command := exec.Command(init.CodexExecutable,
		"exec", "--json", "--color", "never", "--output-last-message", init.ResultPath, "-")
	command.Dir = init.Worktree
	configureExistingProcessGroup(command, groupID)
	stdin, err := command.StdinPipe()
	if err != nil {
		return finishSupervisorStartFailure(anchor, anchorIdentity, writer, fmt.Errorf("open Codex stdin: %w", err))
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return finishSupervisorStartFailure(anchor, anchorIdentity, writer, fmt.Errorf("open Codex stdout: %w", err))
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return finishSupervisorStartFailure(anchor, anchorIdentity, writer, fmt.Errorf("open Codex stderr: %w", err))
	}
	if err := command.Start(); err != nil {
		return finishSupervisorStartFailure(anchor, anchorIdentity, writer, fmt.Errorf("start Codex: %w", err))
	}
	promptErrors := make(chan error, 1)
	go func() {
		_, writeErr := io.WriteString(stdin, init.Prompt)
		closeErr := stdin.Close()
		promptErrors <- errors.Join(writeErr, closeErr)
	}()
	stderrTail := &tailBuffer{limit: protocol.MaxErrorBytes}
	readers := sync.WaitGroup{}
	readers.Add(2)
	go func() {
		defer readers.Done()
		streamSupervisorOutput(stdout, "stdout", writer, nil)
	}()
	go func() {
		defer readers.Done()
		streamSupervisorOutput(stderr, "stderr", writer, stderrTail)
	}()
	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()
	taskTimer := time.NewTimer(time.Duration(init.TimeoutSeconds) * time.Second)
	defer taskTimer.Stop()

	reason := "exited"
	var waitErr error
	running := true
	for running {
		select {
		case command := <-commands:
			action, duration, parseErr := parseControlCommand(command)
			if parseErr != nil {
				reason = "supervisor_error"
				waitErr = parseErr
				running = false
				continue
			}
			switch action {
			case "renew":
				resetTimer(leaseTimer, leaseStopDelay(duration))
			case "cancel":
				reason = "cancelled"
				running = false
			case "lease_lost":
				reason = "lease_lost"
				running = false
			case "fail":
				reason = "supervisor_error"
				running = false
			case "timeout":
				reason = "timeout"
				running = false
			case "start":
			}
		case controlErr := <-controlErrors:
			reason = "parent_lost"
			if controlErr != nil && !errors.Is(controlErr, io.EOF) {
				waitErr = controlErr
			}
			running = false
		case <-leaseTimer.C:
			reason = "lease_lost"
			running = false
		case <-taskTimer.C:
			reason = "timeout"
			running = false
		case waitErr = <-wait:
			running = false
		}
	}

	if reason == "exited" {
		_ = stopOwnedProcessGroup(groupID, anchorIdentity, 0)
	} else {
		stopErr := stopOwnedProcessGroup(groupID, anchorIdentity, terminationGrace)
		if stopErr != nil && waitErr == nil {
			waitErr = stopErr
		}
		select {
		case childErr := <-wait:
			if waitErr == nil {
				waitErr = childErr
			}
		case <-time.After(2 * time.Second):
			if waitErr == nil {
				waitErr = errors.New("Codex process did not reap after group termination")
			}
		}
	}
	_ = anchor.Wait()
	readers.Wait()
	select {
	case promptErr := <-promptErrors:
		if promptErr != nil && !errors.Is(promptErr, os.ErrClosed) && reason == "exited" && waitErr == nil {
			waitErr = fmt.Errorf("send prompt to Codex: %w", promptErr)
		}
	default:
	}

	result, truncated, resultErr := readBoundedText(init.ResultPath, protocol.MaxResultBytes)
	if resultErr != nil && reason == "exited" && waitErr == nil {
		waitErr = resultErr
	}
	_ = os.Remove(init.ResultPath)
	message := supervisorMessage{
		Type:      "exit",
		ExitCode:  exitCode(waitErr),
		Reason:    reason,
		Result:    result,
		Truncated: truncated,
	}
	if reason != "exited" {
		message.Error = map[string]string{
			"cancelled":        "attempt cancelled",
			"parent_lost":      "worker parent process exited",
			"lease_lost":       "control-plane lease renewal deadline passed",
			"timeout":          "task timeout reached",
			"supervisor_error": "attempt supervisor failed",
		}[reason]
	}
	if message.Error == "" && waitErr != nil {
		message.Error = boundedText(waitErr.Error(), protocol.MaxErrorBytes)
	}
	if tail := stderrTail.String(); tail != "" && message.ExitCode != 0 && reason == "exited" {
		if message.Error == "" {
			message.Error = boundedText(tail, protocol.MaxErrorBytes)
		} else {
			message.Error = boundedText(message.Error+": "+tail, protocol.MaxErrorBytes)
		}
	}
	return writer.send(message)
}

func finishSupervisorStartFailure(anchor *exec.Cmd, identity string, writer *synchronizedEncoder, cause error) error {
	_ = stopOwnedProcessGroup(anchor.Process.Pid, identity, 0)
	_ = anchor.Wait()
	return writer.send(supervisorMessage{
		Type: "exit", ExitCode: -1, Reason: "supervisor_error", Error: boundedText(cause.Error(), protocol.MaxErrorBytes),
	})
}

type synchronizedEncoder struct {
	mutex   sync.Mutex
	encoder *json.Encoder
}

func (writer *synchronizedEncoder) send(message supervisorMessage) error {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	return writer.encoder.Encode(message)
}

func readSupervisorControl(control io.Reader, commands chan<- string, errorsChannel chan<- error) {
	scanner := bufio.NewScanner(control)
	for scanner.Scan() {
		commands <- strings.TrimSpace(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		errorsChannel <- err
	} else {
		errorsChannel <- io.EOF
	}
}

func parseControlCommand(command string) (string, time.Duration, error) {
	fields := strings.Fields(command)
	if len(fields) == 1 {
		switch fields[0] {
		case "start", "cancel", "lease_lost", "fail", "timeout":
			return fields[0], 0, nil
		}
	}
	if len(fields) == 2 && fields[0] == "renew" {
		milliseconds, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil && milliseconds > 0 && milliseconds <= protocol.LeaseDuration.Milliseconds() {
			return "renew", time.Duration(milliseconds) * time.Millisecond, nil
		}
	}
	return "", 0, fmt.Errorf("unknown supervisor command %q", command)
}

func streamSupervisorOutput(reader io.Reader, stream string, writer *synchronizedEncoder, tail *tailBuffer) {
	buffered := bufio.NewReaderSize(reader, 32<<10)
	for {
		line, prefix, err := buffered.ReadLine()
		if len(line) > 0 {
			if tail != nil {
				_, _ = tail.Write(line)
				_, _ = tail.Write([]byte{'\n'})
			}
			text := boundedText(string(line), maxSupervisorLineBytes)
			_ = writer.send(supervisorMessage{Type: "output", Stream: stream, Text: text, Truncated: prefix})
		}
		if err != nil {
			return
		}
	}
}

type tailBuffer struct {
	mutex sync.Mutex
	bytes []byte
	limit int
}

func (buffer *tailBuffer) Write(value []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	buffer.bytes = append(buffer.bytes, value...)
	if len(buffer.bytes) > buffer.limit {
		buffer.bytes = append([]byte(nil), buffer.bytes[len(buffer.bytes)-buffer.limit:]...)
	}
	return len(value), nil
}

func (buffer *tailBuffer) String() string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return boundedText(string(buffer.bytes), buffer.limit)
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func leaseStopDelay(remaining time.Duration) time.Duration {
	delay := remaining - terminationGrace
	if delay < time.Millisecond {
		return time.Millisecond
	}
	return delay
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return -1
}

func readBoundedText(path string, limit int) (string, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read Codex result: %w", err)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return "", false, fmt.Errorf("read Codex result: %w", err)
	}
	truncated := len(body) > limit
	if truncated {
		body = body[:limit]
	}
	return boundedText(string(body), limit), truncated, nil
}

func boundedText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func startSupervisor(commandLine []string, init supervisorInit, errorOutput io.Writer) (*supervisorProcess, error) {
	if len(commandLine) == 0 {
		return nil, errors.New("supervisor command is empty")
	}
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create supervisor control pipe: %w", err)
	}
	command := exec.Command(commandLine[0], commandLine[1:]...)
	command.ExtraFiles = []*os.File{controlRead}
	stdin, err := command.StdinPipe()
	if err != nil {
		controlRead.Close()
		controlWrite.Close()
		return nil, fmt.Errorf("open supervisor input: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		controlRead.Close()
		controlWrite.Close()
		return nil, fmt.Errorf("open supervisor output: %w", err)
	}
	stderr := newLimitBuffer(maxSupervisorErrorBytes)
	command.Stderr = io.MultiWriter(errorOutput, stderr)
	if err := command.Start(); err != nil {
		controlRead.Close()
		controlWrite.Close()
		return nil, fmt.Errorf("start attempt supervisor: %w", err)
	}
	controlRead.Close()
	process := &supervisorProcess{
		command:      command,
		control:      controlWrite,
		messages:     make(chan supervisorMessage, 128),
		decodeErrors: make(chan error, 1),
		wait:         make(chan error, 1),
		stderr:       stderr,
	}
	go func() {
		encoder := json.NewEncoder(stdin)
		err := encoder.Encode(init)
		closeErr := stdin.Close()
		if err != nil {
			process.decodeErrors <- errors.Join(err, closeErr)
		}
	}()
	go func() {
		// Keep draining the supervisor protocol for the complete attempt. Progress
		// is bounded downstream, while stopping reads here could block the
		// supervisor before it reports or reaps a terminal process.
		decoder := json.NewDecoder(stdout)
		for {
			var message supervisorMessage
			if err := decoder.Decode(&message); err != nil {
				process.decodeErrors <- err
				return
			}
			process.messages <- message
		}
	}()
	go func() {
		process.wait <- command.Wait()
	}()
	return process, nil
}

func (process *supervisorProcess) awaitReady(ctx context.Context) error {
	timer := time.NewTimer(supervisorReadyTimeout)
	defer timer.Stop()
	select {
	case message := <-process.messages:
		if message.Type != "ready" || message.SupervisorPID <= 0 || message.ProcessGroupID <= 0 ||
			message.ProcessIdentity == "" || message.GroupIdentity == "" {
			return errors.New("attempt supervisor returned invalid readiness data")
		}
		process.supervisorPID = message.SupervisorPID
		process.supervisorIdentity = message.ProcessIdentity
		process.processGroupID = message.ProcessGroupID
		process.groupIdentity = message.GroupIdentity
		return nil
	case err := <-process.decodeErrors:
		return fmt.Errorf("read attempt supervisor readiness: %w: %s", err, process.stderr.String())
	case err := <-process.wait:
		return fmt.Errorf("attempt supervisor exited before readiness: %w: %s", err, process.stderr.String())
	case <-timer.C:
		return errors.New("attempt supervisor did not become ready within ten seconds")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (process *supervisorProcess) send(command string) error {
	process.controlMutex.Lock()
	defer process.controlMutex.Unlock()
	if process.control == nil {
		return errors.New("attempt supervisor control pipe is closed")
	}
	_, err := io.WriteString(process.control, command+"\n")
	return err
}

func (process *supervisorProcess) renew(expiry time.Time) error {
	remaining := time.Until(expiry)
	if remaining <= 0 {
		return process.send("lease_lost")
	}
	if remaining > protocol.LeaseDuration {
		remaining = protocol.LeaseDuration
	}
	milliseconds := remaining.Milliseconds()
	if milliseconds < 1 {
		milliseconds = 1
	}
	return process.send("renew " + strconv.FormatInt(milliseconds, 10))
}

func (process *supervisorProcess) closeControl() error {
	process.controlMutex.Lock()
	defer process.controlMutex.Unlock()
	if process.control == nil {
		return nil
	}
	err := process.control.Close()
	process.control = nil
	return err
}

func supervisorCommandLine(executable string) []string {
	return []string{executable, supervisorCommand}
}

func resultPath(dataDirectory, attemptID string) (string, error) {
	directory := filepath.Join(dataDirectory, "tmp")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create worker temporary directory: %w", err)
	}
	if info, err := os.Lstat(directory); err != nil {
		return "", fmt.Errorf("inspect worker temporary directory: %w", err)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("worker temporary directory must be a real directory, not a symlink")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", fmt.Errorf("protect worker temporary directory: %w", err)
	}
	if !uuidPattern.MatchString(attemptID) {
		return "", errors.New("invalid attempt ID for result path")
	}
	path := filepath.Join(directory, attemptID+"-result")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create Codex result file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close Codex result file: %w", err)
	}
	return path, nil
}

func supervisorStartRequest(process *supervisorProcess, token string) protocol.StartAttemptRequest {
	supervisorPID := process.supervisorPID
	processGroupID := process.processGroupID
	return protocol.StartAttemptRequest{
		LeaseToken:      token,
		SupervisorPID:   &supervisorPID,
		ProcessIdentity: process.supervisorIdentity,
		ProcessGroupID:  &processGroupID,
	}
}

func processSummary(process *supervisorProcess) string {
	return "supervisor=" + strconv.FormatInt(process.supervisorPID, 10) +
		" process_group=" + strconv.FormatInt(process.processGroupID, 10)
}

package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/controlplane"
	"github.com/owainlewis/factory/internal/protocol"
	"golang.org/x/sys/unix"
)

const fakeCodexScript = `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then
  echo "codex-cli test-1.0"
  exit 0
fi
if [ "${1:-}" = "login" ] && [ "${2:-}" = "status" ]; then
  echo "Logged in using test credentials"
  exit 0
fi
if [ "${1:-}" != "exec" ]; then
  echo "unexpected fake Codex arguments" >&2
  exit 90
fi
result=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then
    result="$2"
    shift 2
  else
    shift
  fi
done
prompt="$(cat)"
attempt="$(basename "$PWD")"
mkdir -p "$FACTORY_TEST_CODEX_LOG"
printf '%s' "$prompt" > "$FACTORY_TEST_CODEX_LOG/$attempt.prompt"
echo "$$" > "$FACTORY_TEST_CODEX_LOG/$attempt.pid"
echo '{"type":"thread.started","thread_id":"test-thread"}'
case "$prompt" in
  *FAKE_MODE=success*)
    printf 'completed by fake Codex' > "$result"
    echo '{"type":"item.completed","item":{"type":"agent_message","text":"completed"}}'
    ;;
  *FAKE_MODE=long*)
    head -c 68157440 /dev/zero | tr '\000' x
    echo
    head -c 300000 /dev/zero | tr '\000' r > "$result"
    ;;
  *FAKE_MODE=large-json*)
    printf 'large JSON completed' > "$result"
    printf '{"type":"item.completed","text":"'
    head -c 40000 /dev/zero | tr '\000' x
    printf '"}\n'
    ;;
  *FAKE_MODE=fail*)
    echo "deterministic failure" >&2
    exit 17
    ;;
  *FAKE_MODE=dirty*)
    echo "dirty" > worker-output.txt
    printf 'dirty success' > "$result"
    ;;
  *FAKE_MODE=unpublished*)
    echo "unpublished" > worker-output.txt
    git add worker-output.txt
    git commit -m "fake unpublished result" >/dev/null
    printf 'unpublished success' > "$result"
    ;;
  *FAKE_MODE=hang*)
    trap '' TERM
    while :; do sleep 1; done
    ;;
  *FAKE_MODE=fork*)
    child="$FACTORY_TEST_CODEX_LOG/$attempt.child"
    sh -c 'trap "" TERM; echo $$ > "$1"; while :; do sleep 1; done' sh "$child" &
    trap '' TERM
    wait
    ;;
  *FAKE_MODE=descendant*)
    child="$FACTORY_TEST_CODEX_LOG/$attempt.child"
    sh -c 'trap "" TERM; while :; do sleep 1; done' &
    echo "$!" > "$child"
    printf 'leader completed' > "$result"
    ;;
  *)
    echo "missing fake mode" >&2
    exit 91
    ;;
esac
`

const fakeClaudeScript = `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then
  echo "2.1.220 (Claude Code)"
  exit 0
fi
if [ "${1:-}" = "auth" ] && [ "${2:-}" = "status" ] && [ "${3:-}" = "--json" ]; then
  echo '{"loggedIn":true}'
  exit 0
fi
if [ "${1:-}" != "--print" ] ||
   [ "${2:-}" != "--output-format" ] ||
   [ "${3:-}" != "stream-json" ] ||
   [ "${4:-}" != "--verbose" ] ||
   [ "${5:-}" != "--permission-mode" ] ||
   [ "${6:-}" != "bypassPermissions" ]; then
  echo "unexpected fake Claude Code arguments" >&2
  exit 90
fi
prompt="$(cat)"
if [ "$prompt" != "complete this task" ]; then
  echo "unexpected fake Claude Code prompt" >&2
  exit 91
fi
if [ "${FACTORY_TEST_CLAUDE_OVERSIZED_RESULT:-}" = "1" ]; then
  printf '{"type":"result","subtype":"success","result":"'
  head -c 1100000 /dev/zero | tr '\000' x
  printf '"}\n'
  exit 0
fi
echo '{"type":"system","subtype":"init"}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}'
echo '{"type":"result","subtype":"success","result":"completed by fake Claude Code"}'
`

func TestWorkerSupervisorHelperProcess(t *testing.T) {
	if os.Getenv("FACTORY_TEST_SUPERVISOR") != "1" {
		return
	}
	control := os.NewFile(3, "factory-test-control")
	err := RunSupervisor(control, os.Stdin, os.Stdout, os.Stderr)
	runtime.KeepAlive(control)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

type serverFixture struct {
	store  *controlplane.Store
	server *httptest.Server
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func newServerFixture(t *testing.T, wrap func(http.Handler) http.Handler) *serverFixture {
	t.Helper()
	store, err := controlplane.Open(context.Background(), filepath.Join(t.TempDir(), "factory.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	handler := controlplane.NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if wrap != nil {
		handler = wrap(handler)
	}
	server := httptest.NewServer(handler)
	fixture := &serverFixture{store: store, server: server}
	t.Cleanup(func() {
		server.Close()
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return fixture
}

type repositoryFixture struct {
	path   string
	origin string
}

func createRepository(t *testing.T, name string) repositoryFixture {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, name+"-origin.git")
	path := filepath.Join(root, name)
	runGitTest(t, "", "init", "--bare", "--initial-branch=main", origin)
	runGitTest(t, "", "init", "--initial-branch=main", path)
	runGitTest(t, path, "config", "user.name", "Factory Test")
	runGitTest(t, path, "config", "user.email", "factory@example.invalid")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte(name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, path, "add", "README.md")
	runGitTest(t, path, "commit", "-m", "initial")
	runGitTest(t, path, "remote", "add", "origin", origin)
	runGitTest(t, path, "push", "-u", "origin", "main")
	return repositoryFixture{path: path, origin: origin}
}

func TestWorktreeUsesRemoteDefaultBranchWithoutChangingCheckout(t *testing.T) {
	repository := createRepository(t, "remote-default")
	runGitTest(t, repository.path, "checkout", "-b", "local-only")
	if err := os.WriteFile(filepath.Join(repository.path, "local-only.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository.path, "add", "local-only.txt")
	runGitTest(t, repository.path, "commit", "-m", "local only")
	localCommit := runGitTest(t, repository.path, "rev-parse", "HEAD")

	publisher := filepath.Join(t.TempDir(), "publisher")
	runGitTest(t, "", "clone", repository.origin, publisher)
	runGitTest(t, publisher, "config", "user.name", "Factory Publisher")
	runGitTest(t, publisher, "config", "user.email", "publisher@example.invalid")
	if err := os.WriteFile(filepath.Join(publisher, "remote-only.txt"), []byte("remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, publisher, "add", "remote-only.txt")
	runGitTest(t, publisher, "commit", "-m", "advance remote")
	runGitTest(t, publisher, "push", "origin", "main")
	mainCommit := runGitTest(t, publisher, "rev-parse", "HEAD")
	if stale := runGitTest(t, repository.path, "rev-parse", "refs/remotes/origin/main"); stale == mainCommit {
		t.Fatal("registered checkout unexpectedly observed the remote commit before workspace preparation")
	}

	resolved, err := resolveRepository("factory", repository.path, "git")
	if err != nil {
		t.Fatal(err)
	}
	value, err := createWorktree(
		context.Background(), "git", filepath.Join(t.TempDir(), "worktrees"),
		resolved, fixtureUUID(700), fixtureUUID(701),
	)
	if err != nil {
		t.Fatal(err)
	}
	if value.BaseBranch != "main" || value.BaseCommit != mainCommit || value.BaseCommit == localCommit {
		t.Fatalf("worktree base = %q %q; want main %q, not local %q",
			value.BaseBranch, value.BaseCommit, mainCommit, localCommit)
	}
	if branch := runGitTest(t, repository.path, "branch", "--show-current"); branch != "local-only" {
		t.Fatalf("configured checkout branch = %q; want local-only", branch)
	}
	if _, err := os.Stat(filepath.Join(value.Path, "local-only.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("isolated worktree contains local-only change: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(value.Path, "remote-only.txt")); err != nil || string(body) != "remote\n" {
		t.Fatalf("isolated worktree did not fetch the authoritative remote commit: %q, %v", body, err)
	}
}

func TestWorktreeUsesConfiguredBaseBranch(t *testing.T) {
	repository := createRepository(t, "configured-base")
	runGitTest(t, repository.path, "checkout", "-b", "release/2026.07")
	if err := os.WriteFile(filepath.Join(repository.path, "release.txt"), []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository.path, "add", "release.txt")
	runGitTest(t, repository.path, "commit", "-m", "release")
	runGitTest(t, repository.path, "push", "origin", "release/2026.07")
	releaseCommit := runGitTest(t, repository.path, "rev-parse", "HEAD")
	runGitTest(t, repository.path, "checkout", "main")

	resolved, err := resolveRepository("factory", repository.path, "git")
	if err != nil {
		t.Fatal(err)
	}
	resolved.BaseBranch = "release/2026.07"
	value, err := createWorktree(
		context.Background(), "git", filepath.Join(t.TempDir(), "worktrees"),
		resolved, fixtureUUID(702), fixtureUUID(703),
	)
	if err != nil {
		t.Fatal(err)
	}
	if value.BaseBranch != "release/2026.07" || value.BaseCommit != releaseCommit {
		t.Fatalf("worktree base = %q %q; want release/2026.07 %q",
			value.BaseBranch, value.BaseCommit, releaseCommit)
	}
	if body, err := os.ReadFile(filepath.Join(value.Path, "release.txt")); err != nil || string(body) != "release\n" {
		t.Fatalf("release worktree file = %q, %v", body, err)
	}
	if branch := runGitTest(t, repository.path, "branch", "--show-current"); branch != "main" {
		t.Fatalf("configured checkout branch = %q; want main", branch)
	}
}

func TestWorktreeRejectsMissingConfiguredBaseBranch(t *testing.T) {
	repository := createRepository(t, "missing-base")
	resolved, err := resolveRepository("factory", repository.path, "git")
	if err != nil {
		t.Fatal(err)
	}
	resolved.BaseBranch = "release/missing"
	root := filepath.Join(t.TempDir(), "worktrees")
	attemptID := fixtureUUID(705)
	_, err = prepareWorktree(
		context.Background(), "git", root, resolved, fixtureUUID(704), attemptID,
	)
	if err == nil || !strings.Contains(err.Error(), "base branch origin/release/missing does not exist") {
		t.Fatalf("missing base branch error = %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, attemptID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing base branch created a worktree path: %v", statErr)
	}
}

func TestWorktreeRejectsOriginChangedAfterRegistration(t *testing.T) {
	repository := createRepository(t, "registered-origin")
	other := createRepository(t, "replacement-origin")
	resolved, err := resolveRepository("factory", repository.path, "git")
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository.path, "remote", "set-url", "origin", other.origin)
	root := filepath.Join(t.TempDir(), "worktrees")
	attemptID := fixtureUUID(707)
	_, err = prepareWorktree(
		context.Background(), "git", root, resolved, fixtureUUID(706), attemptID,
	)
	if err == nil || !strings.Contains(err.Error(), "repository origin changed since worker registration") {
		t.Fatalf("changed origin error = %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, attemptID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("changed origin created a worktree path: %v", statErr)
	}
}

func TestWorktreeRejectsOriginChangedDuringBaseResolution(t *testing.T) {
	repository := createRepository(t, "origin-race")
	other := createRepository(t, "origin-race-replacement")
	resolved, err := resolveRepository("factory", repository.path, "git")
	if err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_TEST_REAL_GIT", realGit)
	t.Setenv("FACTORY_TEST_REPLACEMENT_ORIGIN", other.origin)
	wrapper := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(wrapper, []byte(`#!/bin/sh
"$FACTORY_TEST_REAL_GIT" "$@"
status=$?
if [ "$status" -eq 0 ] && [ "$1" = "ls-remote" ] && [ "$2" = "--symref" ]; then
  "$FACTORY_TEST_REAL_GIT" remote set-url origin "$FACTORY_TEST_REPLACEMENT_ORIGIN"
fi
exit "$status"
`), 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "worktrees")
	attemptID := fixtureUUID(709)
	_, err = prepareWorktree(
		context.Background(), wrapper, root, resolved, fixtureUUID(708), attemptID,
	)
	if err == nil || !strings.Contains(err.Error(), "repository origin changed since worker registration") {
		t.Fatalf("concurrent origin change error = %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, attemptID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("concurrent origin change created a worktree path: %v", statErr)
	}
}

func TestWorktreeFetchDoesNotApplyConfiguredOriginRefmap(t *testing.T) {
	repository := createRepository(t, "custom-refmap")
	sentinel := runGitTest(t, repository.path, "rev-parse", "HEAD")
	runGitTest(t, repository.path, "fetch", "origin", "main")
	fetchHeadPath := filepath.Join(repository.path, ".git", "FETCH_HEAD")
	fetchHead, err := os.ReadFile(fetchHeadPath)
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository.path, "branch", "vendor/main", sentinel)
	runGitTest(t, repository.path, "config", "remote.origin.fetch", "+refs/heads/*:refs/heads/vendor/*")

	publisher := filepath.Join(t.TempDir(), "publisher")
	runGitTest(t, "", "clone", repository.origin, publisher)
	runGitTest(t, publisher, "config", "user.name", "Factory Publisher")
	runGitTest(t, publisher, "config", "user.email", "publisher@example.invalid")
	if err := os.WriteFile(filepath.Join(publisher, "remote-refmap.txt"), []byte("remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, publisher, "add", "remote-refmap.txt")
	runGitTest(t, publisher, "commit", "-m", "advance refmap remote")
	runGitTest(t, publisher, "push", "origin", "main")
	remoteCommit := runGitTest(t, publisher, "rev-parse", "HEAD")

	resolved, err := resolveRepository("factory", repository.path, "git")
	if err != nil {
		t.Fatal(err)
	}
	value, err := createWorktree(
		context.Background(), "git", filepath.Join(t.TempDir(), "worktrees"),
		resolved, fixtureUUID(710), fixtureUUID(711),
	)
	if err != nil {
		t.Fatal(err)
	}
	if value.BaseCommit != remoteCommit {
		t.Fatalf("worktree base = %q; want remote %q", value.BaseCommit, remoteCommit)
	}
	if branchCommit := runGitTest(t, repository.path, "rev-parse", "refs/heads/vendor/main"); branchCommit != sentinel {
		t.Fatalf("configured fetch mapping changed vendor/main from %q to %q", sentinel, branchCommit)
	}
	if currentFetchHead, err := os.ReadFile(fetchHeadPath); err != nil || string(currentFetchHead) != string(fetchHead) {
		t.Fatalf("Factory base fetch changed FETCH_HEAD from %q to %q: %v", fetchHead, currentFetchHead, err)
	}
}

func runGitTest(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeFakeCodex(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(fakeCodexScript), 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeFakeClaude(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(fakeClaudeScript), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeCodeHealthAndSupervisorContract(t *testing.T) {
	claudePath := filepath.Join(t.TempDir(), "claude")
	writeFakeClaude(t, claudePath)
	value := checkHealth(
		context.Background(), "git", protocol.RuntimeClaudeCode, claudePath, "gh", nil,
	)
	if value.State != "healthy" || value.RuntimeVersion != "2.1.220 (Claude Code)" {
		t.Fatalf("Claude Code health = %#v", value)
	}

	t.Setenv("FACTORY_TEST_SUPERVISOR", "1")
	repository := createRepository(t, "claude-supervisor")
	process, err := startSupervisor(
		[]string{os.Args[0], "-test.run=TestWorkerSupervisorHelperProcess", "--"},
		supervisorInit{
			Runtime:           protocol.RuntimeClaudeCode,
			RuntimeExecutable: claudePath,
			Worktree:          repository.path,
			ResultPath:        filepath.Join(t.TempDir(), "unused-result"),
			Prompt:            "complete this task",
			TimeoutSeconds:    60,
		},
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.awaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := process.send("start"); err != nil {
		t.Fatal(err)
	}
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	sawResultEvent := false
	for {
		select {
		case message := <-process.messages:
			if message.Type == "output" && strings.Contains(message.Text, `"type":"result"`) {
				sawResultEvent = true
			}
			if message.Type != "exit" {
				continue
			}
			if message.ExitCode != 0 || message.Reason != "exited" ||
				message.Result != "completed by fake Claude Code" || !sawResultEvent {
				t.Fatalf("Claude Code supervisor exit = %#v", message)
			}
			return
		case err := <-process.decodeErrors:
			t.Fatalf("decode Claude Code supervisor output: %v", err)
		case <-timeout.C:
			t.Fatal("Claude Code supervisor did not exit")
		}
	}
}

func TestGitHubSourceAccessIsAdvertisedOnlyAfterSuccessfulProbe(t *testing.T) {
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	githubPath := filepath.Join(t.TempDir(), "gh")
	writeProbe := func(exitCode string) {
		t.Helper()
		body := "#!/bin/sh\n" +
			"test \"$*\" = \"auth status --hostname github.com\" || exit 91\n" +
			"exit " + exitCode + "\n"
		if err := os.WriteFile(githubPath, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	writeProbe("0")
	value := checkHealth(
		context.Background(), "git", protocol.RuntimeCodex, codexPath,
		githubPath, nil,
	)
	want := []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}}
	if value.State != "healthy" || !reflect.DeepEqual(value.SourceAccess, want) {
		t.Fatalf("successful GitHub probe health = %#v", value)
	}

	writeProbe("1")
	value = checkHealth(
		context.Background(), "git", protocol.RuntimeCodex, codexPath,
		githubPath, []string{"github"},
	)
	if value.State != "healthy" || len(value.SourceAccess) != 0 {
		t.Fatalf("failed GitHub probe health = %#v", value)
	}
}

func TestZeroRepositoryWorkerAcquiresCentrallyManagedGitHubRepository(t *testing.T) {
	upstream := createRepository(t, "cattle")
	fixture := newServerFixture(t, nil)
	managed, created, err := fixture.store.CreateManagedRepository(
		context.Background(),
		protocol.CreateManagedRepositoryRequest{RemoteIdentity: "github.com/example/cattle"},
	)
	if err != nil || !created {
		t.Fatalf("create managed repository: created %t, err %v", created, err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	toolDirectory := t.TempDir()
	githubPath := filepath.Join(toolDirectory, "gh")
	gitPath := filepath.Join(toolDirectory, "git")
	t.Setenv("FACTORY_TEST_REAL_GIT", realGit)
	t.Setenv("FACTORY_TEST_GH_ORIGIN", upstream.origin)
	githubScript := `#!/bin/sh
set -eu
if [ "${1:-}" = "auth" ] && [ "${2:-}" = "status" ]; then
  exit 0
fi
if [ "${1:-}" != "repo" ] || [ "${2:-}" != "clone" ] || [ "${3:-}" != "example/cattle" ]; then
  exit 91
fi
"$FACTORY_TEST_REAL_GIT" clone --no-checkout "$FACTORY_TEST_GH_ORIGIN" "$4"
"$FACTORY_TEST_REAL_GIT" -C "$4" remote set-url origin https://github.com/example/cattle.git
`
	if err := os.WriteFile(githubPath, []byte(githubScript), 0o700); err != nil {
		t.Fatal(err)
	}
	gitScript := `#!/bin/sh
set -eu
if [ "${1:-}" = "remote" ] && [ "${2:-}" = "get-url" ] && [ "${3:-}" = "origin" ]; then
  case "$PWD" in
    */repositories/*)
      echo https://github.com/example/cattle.git
      exit 0
      ;;
  esac
fi
if [ "${1:-}" = "fetch" ] || [ "${1:-}" = "ls-remote" ]; then
  exec "$FACTORY_TEST_REAL_GIT" -c "url.$FACTORY_TEST_GH_ORIGIN.insteadOf=https://github.com/example/cattle.git" "$@"
fi
exec "$FACTORY_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(gitPath, []byte(gitScript), 0o700); err != nil {
		t.Fatal(err)
	}
	codexPath := filepath.Join(toolDirectory, "codex")
	writeFakeCodex(t, codexPath)
	t.Setenv("FACTORY_TEST_SUPERVISOR", "1")
	logDirectory := filepath.Join(t.TempDir(), "codex-log")
	t.Setenv("FACTORY_TEST_CODEX_LOG", logDirectory)
	dataDirectory := filepath.Join(t.TempDir(), "worker")
	options := testOptions(codexPath)
	options.GitExecutable = gitPath
	options.GitHubExecutable = githubPath
	manager, err := New(Config{
		Server: fixture.server.URL, Name: "cattle-worker", MaxConcurrent: 1,
		DataDirectory: dataDirectory,
	}, options, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	cancelFirst, firstDone := startManager(t, manager)
	worker := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool {
		return worker.Health == "healthy" && worker.AcceptsManagedRepositories &&
			len(worker.Repositories) == 0 && hasGitHubSourceAccess(worker.SourceAccess)
	})

	task, taskCreated, err := fixture.store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "cattle-e2e", Title: "Cattle worker task",
		Description: "Exercise managed repository acquisition.\nFAKE_MODE=success",
		Route: &protocol.TaskRoute{
			RepositoryRemoteIdentity: managed.RemoteIdentity,
			SourceAccess:             protocol.SourceAccess{Provider: "github", Hostname: "github.com"},
		},
		TimeoutSeconds: 60,
	})
	if err != nil || !taskCreated {
		t.Fatalf("create routed task: created %t, err %v", taskCreated, err)
	}
	if task.Execution.AssignedWorkerID != worker.ID {
		t.Fatalf("assigned worker = %q, want %q", task.Execution.AssignedWorkerID, worker.ID)
	}
	task = waitForTaskState(t, fixture.store, task.Task.ID, "succeeded")
	if len(task.Attempts) != 1 || task.Attempts[0].Result != "completed by fake Codex" {
		t.Fatalf("cattle task attempts = %#v", task.Attempts)
	}
	cachePath := filepath.Join(dataDirectory, "repositories", managed.ID)
	if info, err := os.Stat(cachePath); err != nil || !info.IsDir() {
		t.Fatalf("managed repository cache = %v, err %v", info, err)
	}
	sentinel := runGitTest(t, cachePath, "rev-parse", "HEAD")
	runGitTest(t, cachePath, "branch", "vendor/main", sentinel)
	runGitTest(t, cachePath, "config", "remote.origin.fetch", "+refs/heads/*:refs/heads/vendor/*")
	if err := os.WriteFile(filepath.Join(upstream.path, "SECOND.md"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, upstream.path, "add", "SECOND.md")
	runGitTest(t, upstream.path, "commit", "-m", "second")
	runGitTest(t, upstream.path, "push", "origin", "main")
	refreshed, err := manager.acquireManagedRepository(context.Background(), protocol.Repository{
		ID: managed.ID, Key: managed.RemoteIdentity, RemoteIdentity: managed.RemoteIdentity,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := runGitTest(t, upstream.path, "rev-parse", "HEAD"); refreshed.BaseBranch != "main" || refreshed.BaseCommit != want {
		t.Fatalf("refreshed base = %q %q, want main %q", refreshed.BaseBranch, refreshed.BaseCommit, want)
	}
	if branchCommit := runGitTest(t, cachePath, "rev-parse", "refs/heads/vendor/main"); branchCommit != sentinel {
		t.Fatalf("managed fetch mapping changed vendor/main from %q to %q", sentinel, branchCommit)
	}
	worker = waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool {
		return len(worker.Repositories) == 1 && worker.Repositories[0].ID == managed.ID
	})
	if worker.Repositories[0].Key != managed.RemoteIdentity {
		t.Fatalf("dynamic repository = %#v", worker.Repositories[0])
	}
	prompt, err := os.ReadFile(filepath.Join(logDirectory, task.Attempts[0].ID+".prompt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "Repository: "+managed.RemoteIdentity) {
		t.Fatalf("prompt did not identify managed repository:\n%s", prompt)
	}

	cancelFirst()
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	restarted, err := New(Config{
		Server: fixture.server.URL, Name: "cattle-worker", MaxConcurrent: 1,
		DataDirectory: dataDirectory,
	}, options, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if restarted.ID() != manager.ID() {
		t.Fatalf("worker ID changed across restart: %q != %q", restarted.ID(), manager.ID())
	}
	startManager(t, restarted)
	waitForWorker(t, fixture.store, restarted.ID(), func(worker protocol.Worker) bool {
		return worker.Health == "healthy" && worker.AcceptsManagedRepositories &&
			len(worker.Repositories) == 1 && worker.Repositories[0].ID == managed.ID
	})
	restartedTask, restartedCreated, err := fixture.store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "cattle-e2e-restart", Title: "Restarted cattle worker task",
		Description: "Reuse the managed repository cache.\nFAKE_MODE=success",
		Route: &protocol.TaskRoute{
			RepositoryRemoteIdentity: managed.RemoteIdentity,
			SourceAccess:             protocol.SourceAccess{Provider: "github", Hostname: "github.com"},
		},
		TimeoutSeconds: 60,
	})
	if err != nil || !restartedCreated {
		t.Fatalf("create restarted routed task: created %t, err %v", restartedCreated, err)
	}
	restartedTask = waitForTaskState(t, fixture.store, restartedTask.Task.ID, "succeeded")
	if len(restartedTask.Attempts) != 1 || restartedTask.Attempts[0].Result != "completed by fake Codex" {
		t.Fatalf("restarted cattle task attempts = %#v", restartedTask.Attempts)
	}
}

func TestManagedRepositoryCacheEnforcesItsHardLimit(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < protocol.MaxRepositoryCacheEntries; index++ {
		if err := os.Mkdir(filepath.Join(root, fixtureUUID(index+1)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := enforceRepositoryCacheLimit(root); err == nil || !strings.Contains(err.Error(), "cache limit") {
		t.Fatalf("cache limit error = %v", err)
	}
}

func TestManagedRepositoryCacheStartupRemovesInterruptedClones(t *testing.T) {
	dataDirectory := t.TempDir()
	root := filepath.Join(dataDirectory, "repositories")
	staleClone := filepath.Join(root, ".clone-interrupted", "repository")
	if err := os.MkdirAll(staleClone, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleClone, "partial.pack"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	installedID := fixtureUUID(780)
	if err := os.Mkdir(filepath.Join(root, installedID), 0o700); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(root, "operator-note")
	if err := os.Mkdir(unrelated, 0o700); err != nil {
		t.Fatal(err)
	}

	ids, err := managedRepositoryCacheIDs(dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || !ids[installedID] {
		t.Fatalf("managed repository cache IDs = %#v", ids)
	}
	if _, err := os.Stat(filepath.Join(root, ".clone-interrupted")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted clone remains after startup scan: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("startup scan removed an unrelated entry: %v", err)
	}
}

func TestFailedManagedRepositoryCloneLeavesNoCacheEntry(t *testing.T) {
	dataDirectory := t.TempDir()
	githubPath := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(githubPath, []byte("#!/bin/sh\nexit 42\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		dataDirectory: dataDirectory,
		options:       Options{GitExecutable: "git", GitHubExecutable: githubPath},
	}
	repositoryID := fixtureUUID(500)
	_, err := manager.acquireManagedRepository(context.Background(), protocol.Repository{
		ID: repositoryID, Key: "github.com/example/failure",
		RemoteIdentity: "github.com/example/failure",
	})
	if err == nil || !strings.Contains(err.Error(), "clone managed GitHub repository") {
		t.Fatalf("clone failure = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dataDirectory, "repositories", repositoryID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed clone left cache entry: %v", err)
	}
}

func TestClaudeCodeSupervisorAcceptsOversizedResult(t *testing.T) {
	t.Setenv("FACTORY_TEST_SUPERVISOR", "1")
	t.Setenv("FACTORY_TEST_CLAUDE_OVERSIZED_RESULT", "1")
	claudePath := filepath.Join(t.TempDir(), "claude")
	writeFakeClaude(t, claudePath)
	repository := createRepository(t, "claude-oversized-result")
	process, err := startSupervisor(
		[]string{os.Args[0], "-test.run=TestWorkerSupervisorHelperProcess", "--"},
		supervisorInit{
			Runtime:           protocol.RuntimeClaudeCode,
			RuntimeExecutable: claudePath,
			Worktree:          repository.path,
			ResultPath:        filepath.Join(t.TempDir(), "unused-result"),
			Prompt:            "complete this task",
			TimeoutSeconds:    60,
		},
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.awaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := process.send("start"); err != nil {
		t.Fatal(err)
	}
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	sawTruncatedOutput := false
	for {
		select {
		case message := <-process.messages:
			if message.Type == "output" && message.Stream == "stdout" && message.Truncated {
				sawTruncatedOutput = len(message.Text) == maxSupervisorLineBytes
			}
			if message.Type != "exit" {
				continue
			}
			if message.ExitCode != 0 || message.Reason != "exited" || !message.Truncated ||
				len(message.Result) != protocol.MaxResultBytes || !sawTruncatedOutput {
				t.Fatalf("Claude Code oversized result exit = %#v; truncated output = %v",
					message, sawTruncatedOutput)
			}
			if strings.Trim(message.Result, "x") != "" {
				t.Fatal("Claude Code oversized result did not preserve its bounded prefix")
			}
			return
		case err := <-process.decodeErrors:
			t.Fatalf("decode Claude Code supervisor output: %v", err)
		case <-timeout.C:
			t.Fatal("Claude Code oversized result supervisor did not exit")
		}
	}
}

func testOptions(codexPath string) Options {
	return Options{
		GitExecutable:        "git",
		GitHubExecutable:     filepath.Join(filepath.Dir(codexPath), "unavailable-gh"),
		RuntimeExecutable:    codexPath,
		SupervisorCommand:    []string{os.Args[0], "-test.run=TestWorkerSupervisorHelperProcess", "--"},
		WorkerVersion:        "test",
		PollInterval:         20 * time.Millisecond,
		HealthInterval:       50 * time.Millisecond,
		RegistrationInterval: 25 * time.Millisecond,
		LeaseRenewInterval:   100 * time.Millisecond,
		LeaseRetryInterval:   50 * time.Millisecond,
		TransportBackoffMin:  20 * time.Millisecond,
		TransportBackoffMax:  100 * time.Millisecond,
		ShutdownTimeout:      15 * time.Second,
	}
}

func newTestManager(
	t *testing.T,
	fixture *serverFixture,
	codexPath string,
	dataDirectory string,
	repositories map[string]repositoryFixture,
	maxConcurrent int,
) *Manager {
	t.Helper()
	t.Setenv("FACTORY_TEST_SUPERVISOR", "1")
	logDirectory := filepath.Join(t.TempDir(), "codex-log")
	t.Setenv("FACTORY_TEST_CODEX_LOG", logDirectory)
	configured := make(map[string]RepositoryConfig, len(repositories))
	for key, repository := range repositories {
		configured[key] = RepositoryConfig{Path: repository.path}
	}
	manager, err := New(Config{
		Server: fixture.server.URL, Name: "test-worker", MaxConcurrent: maxConcurrent,
		DataDirectory: dataDirectory, Repositories: configured,
	}, testOptions(codexPath), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func startManager(t *testing.T, manager *Manager) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err, ok := <-done:
			if ok && err != nil {
				t.Errorf("worker stopped with error: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Error("worker did not stop")
		}
	})
	return cancel, done
}

func waitForWorker(t *testing.T, store *controlplane.Store, workerID string, predicate func(protocol.Worker) bool) protocol.Worker {
	t.Helper()
	var found protocol.Worker
	waitFor(t, 15*time.Second, func() bool {
		workers, err := store.Workers(context.Background())
		if err != nil {
			return false
		}
		for _, worker := range workers {
			if worker.ID == workerID {
				found = worker
				return predicate(worker)
			}
		}
		return false
	})
	return found
}

func createTask(t *testing.T, store *controlplane.Store, worker protocol.Worker, repositoryKey, mode string, timeout int) protocol.TaskDetail {
	t.Helper()
	var repositoryID string
	for _, repository := range worker.Repositories {
		if repository.Key == repositoryKey {
			repositoryID = repository.ID
		}
	}
	if repositoryID == "" {
		t.Fatalf("worker did not advertise repository %q", repositoryKey)
	}
	task, _, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey:  "request-" + repositoryKey + "-" + mode + "-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		Title:       "Task " + mode,
		Description: "Exercise worker behavior.\nFAKE_MODE=" + mode,
		WorkerID:    worker.ID, RepositoryID: repositoryID, TimeoutSeconds: timeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func waitForTaskState(t *testing.T, store *controlplane.Store, taskID string, states ...string) protocol.TaskDetail {
	t.Helper()
	expected := make(map[string]bool)
	for _, state := range states {
		expected[state] = true
	}
	var detail protocol.TaskDetail
	waitFor(t, 75*time.Second, func() bool {
		value, err := store.Task(context.Background(), taskID)
		if err != nil {
			return false
		}
		detail = value
		if isTerminalState(value.Execution.State) && !expected[value.Execution.State] {
			t.Fatalf("task reached unexpected terminal state %q: %#v", value.Execution.State, value.Attempts)
		}
		return expected[value.Execution.State]
	})
	return detail
}

func isTerminalState(state string) bool {
	return state == "succeeded" || state == "failed" || state == "cancelled"
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition did not become true before timeout")
}

func TestConfigurationStableIdentityLockAndHealthRecovery(t *testing.T) {
	repository := createRepository(t, "health")
	fixture := newServerFixture(t, nil)
	codexPath := filepath.Join(t.TempDir(), "codex")
	gitPath := filepath.Join(t.TempDir(), "git")
	dataDirectory := filepath.Join(t.TempDir(), "worker")
	manager := newTestManager(t, fixture, codexPath, dataDirectory, map[string]repositoryFixture{"health": repository}, 1)
	manager.options.GitExecutable = gitPath
	firstID := manager.ID()
	lockedOptions := testOptions(codexPath)
	lockedOptions.GitExecutable = gitPath
	_, err := New(manager.config, lockedOptions, nil)
	if err == nil || !strings.Contains(err.Error(), "already owns") {
		t.Fatalf("second manager lock error = %v", err)
	}
	cancel, done := startManager(t, manager)
	waitForWorker(t, fixture.store, firstID, func(worker protocol.Worker) bool {
		return worker.Health == "unhealthy"
	})
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gitShim := "#!/bin/sh\nexec " + strconv.Quote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(gitPath, []byte(gitShim), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFakeCodex(t, codexPath)
	waitForWorker(t, fixture.store, firstID, func(worker protocol.Worker) bool {
		return worker.Health == "healthy" && worker.RuntimeVersion == "codex-cli test-1.0"
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	manager2 := newTestManager(t, fixture, codexPath, dataDirectory, map[string]repositoryFixture{"health": repository}, 1)
	if manager2.ID() != firstID {
		t.Fatalf("worker ID changed across restart: %s != %s", manager2.ID(), firstID)
	}
	if err := manager2.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryRejectedByGitCannotUseConfigFallback(t *testing.T) {
	path := t.TempDir()
	if err := os.Mkdir(filepath.Join(path, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "[remote \"origin\"]\n\turl = https://github.com/example/not-a-repository.git\n"
	if err := os.WriteFile(filepath.Join(path, ".git", "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveRepository("invalid", path, "git"); err == nil ||
		!strings.Contains(err.Error(), "verify Git repository") {
		t.Fatalf("invalid repository error = %v", err)
	}
}

func TestUnavailableGitExecutableUsesConfigFallback(t *testing.T) {
	fixture := createRepository(t, "missing-git")
	repository, err := resolveRepository("missing-git", fixture.path, filepath.Join(t.TempDir(), "git"))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := normalizeRemoteIdentity(fixture.origin, fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	if repository.RemoteIdentity != expected {
		t.Fatalf("remote identity = %q; want %q", repository.RemoteIdentity, expected)
	}
}

func TestMultiRepositorySuccessAndBoundedOutput(t *testing.T) {
	first := createRepository(t, "first")
	second := createRepository(t, "second")
	fixture := newServerFixture(t, nil)
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"first": first, "second": second}, 2)
	startManager(t, manager)
	worker := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool {
		return worker.Health == "healthy" && len(worker.Repositories) == 2
	})
	success := createTask(t, fixture.store, worker, "first", "success", 60)
	long := createTask(t, fixture.store, worker, "second", "long", 60)
	success = waitForTaskState(t, fixture.store, success.Task.ID, "succeeded")
	long = waitForTaskState(t, fixture.store, long.Task.ID, "succeeded")
	if len(success.Attempts) != 1 || len(long.Attempts) != 1 {
		t.Fatalf("attempt counts = %d, %d", len(success.Attempts), len(long.Attempts))
	}
	if success.Attempts[0].Result != "completed by fake Codex" {
		t.Fatalf("success result = %q", success.Attempts[0].Result)
	}
	if len(long.Attempts[0].Result) != protocol.MaxResultBytes {
		t.Fatalf("bounded result length = %d", len(long.Attempts[0].Result))
	}
	eventPage, err := fixture.store.Events(context.Background(), long.Attempts[0].ID, -1, protocol.DefaultEventPageSize)
	if err != nil {
		t.Fatal(err)
	}
	events := eventPage.Events
	if len(events) == 0 {
		t.Fatal("long Codex output produced no stored events")
	}
	for _, event := range events {
		if len(event.Payload) > protocol.MaxEventBytes {
			t.Fatalf("event payload exceeded limit: %d", len(event.Payload))
		}
	}
	for _, attempt := range []protocol.Attempt{success.Attempts[0], long.Attempts[0]} {
		path := filepath.Join(manager.dataDirectory, "worktrees", attempt.ID)
		waitFor(t, 5*time.Second, func() bool {
			_, err := os.Stat(path)
			return errors.Is(err, os.ErrNotExist)
		})
		branch := "factory/" + map[string]string{
			success.Attempts[0].ID: success.Task.ID,
			long.Attempts[0].ID:    long.Task.ID,
		}[attempt.ID][:12] + "-" + attempt.ID[:12]
		repositoryPath := map[string]string{
			success.Attempts[0].ID: first.path,
			long.Attempts[0].ID:    second.path,
		}[attempt.ID]
		waitFor(t, 5*time.Second, func() bool {
			command := exec.Command("git", "show-ref", "--verify", "refs/heads/"+branch)
			command.Dir = repositoryPath
			return command.Run() != nil
		})
	}
	prompt, err := os.ReadFile(filepath.Join(os.Getenv("FACTORY_TEST_CODEX_LOG"), success.Attempts[0].ID+".prompt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"Factory managed Git worktree",
		"Task title: Task success",
		"Repository: " + repositoryIdentity(t, first.path),
		"FAKE_MODE=success",
	} {
		if !strings.Contains(string(prompt), expected) {
			t.Fatalf("Codex prompt does not contain %q:\n%s", expected, prompt)
		}
	}
}

func TestLargeCodexJSONLineIsPreserved(t *testing.T) {
	repository := createRepository(t, "large-json")
	fixture := newServerFixture(t, nil)
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"large-json": repository}, 1)
	startManager(t, manager)
	worker := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool {
		return worker.Health == "healthy"
	})
	task := createTask(t, fixture.store, worker, "large-json", "large-json", 60)
	task = waitForTaskState(t, fixture.store, task.Task.ID, "succeeded")
	eventPage, err := fixture.store.Events(context.Background(), task.Attempts[0].ID, -1, protocol.DefaultEventPageSize)
	if err != nil {
		t.Fatal(err)
	}
	events := eventPage.Events
	for _, event := range events {
		var payload struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Type == "item.completed" {
			if payload.Text != strings.Repeat("x", 40000) {
				t.Fatalf("large JSON text length = %d", len(payload.Text))
			}
			return
		}
	}
	t.Fatal("large JSON event was not stored")
}

func TestRepositoryKeyRemapStopsStableWorkerFromClaiming(t *testing.T) {
	first := createRepository(t, "first")
	second := createRepository(t, "second")
	fixture := newServerFixture(t, nil)
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	dataDirectory := filepath.Join(t.TempDir(), "worker")

	firstManager := newTestManager(t, fixture, codexPath, dataDirectory,
		map[string]repositoryFixture{"stable": first}, 1)
	cancelFirst, firstDone := startManager(t, firstManager)
	worker := waitForWorker(t, fixture.store, firstManager.ID(), func(worker protocol.Worker) bool {
		return worker.Health == "healthy" && len(worker.Repositories) == 1
	})
	cancelFirst()
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}

	secondManager := newTestManager(t, fixture, codexPath, dataDirectory,
		map[string]repositoryFixture{"stable": second}, 1)
	startManager(t, secondManager)
	waitFor(t, 5*time.Second, func() bool {
		secondManager.stateMutex.Lock()
		defer secondManager.stateMutex.Unlock()
		return secondManager.fatalHealth != nil && !secondManager.registered
	})

	task := createTask(t, fixture.store, worker, "stable", "success", 60)
	time.Sleep(250 * time.Millisecond)
	detail, err := fixture.store.Task(context.Background(), task.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Execution.State != "queued" || len(detail.Attempts) != 0 {
		t.Fatalf("worker claimed after rejected repository remap: state=%s attempts=%d",
			detail.Execution.State, len(detail.Attempts))
	}
}

func TestHealthFailureCancelsRetryingClaimBeforeServerRecovery(t *testing.T) {
	claimStarted := make(chan struct{})
	releaseClaim := make(chan struct{})
	var blocked atomic.Bool
	fixture := newServerFixture(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if strings.HasSuffix(request.URL.Path, "/claims") && blocked.CompareAndSwap(false, true) {
				close(claimStarted)
				<-releaseClaim
			}
			next.ServeHTTP(w, request)
		})
	})
	repository := createRepository(t, "health-claim")
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"health": repository}, 1)
	manager.options.HealthInterval = 20 * time.Millisecond
	manager.options.RegistrationInterval = 5 * time.Second
	startManager(t, manager)
	worker := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool {
		return worker.Health == "healthy"
	})
	select {
	case <-claimStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("claim did not start")
	}
	task := createTask(t, fixture.store, worker, "health", "success", 60)
	if err := os.Remove(codexPath); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		manager.stateMutex.Lock()
		defer manager.stateMutex.Unlock()
		return manager.health.State == "unhealthy"
	})
	close(releaseClaim)

	time.Sleep(300 * time.Millisecond)
	detail, err := fixture.store.Task(context.Background(), task.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Execution.State != "queued" || len(detail.Attempts) != 0 {
		t.Fatalf("unhealthy worker completed a pending claim: state=%s attempts=%d",
			detail.Execution.State, len(detail.Attempts))
	}
}

func TestCommittedClaimBecomesFailedWhenHealthChangesBeforeResponse(t *testing.T) {
	fixture := newServerFixture(t, nil)
	repository := createRepository(t, "committed-claim")
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"health": repository}, 1)
	manager.options.HealthInterval = 20 * time.Millisecond
	manager.options.RegistrationInterval = 5 * time.Second

	claimCommitted := make(chan struct{})
	releaseResponse := make(chan struct{})
	var blocked atomic.Bool
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	manager.client.http = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(request)
		if err != nil || !strings.HasSuffix(request.URL.Path, "/claims") ||
			response.StatusCode != http.StatusOK || !blocked.CompareAndSwap(false, true) {
			return response, err
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		response.Body = io.NopCloser(bytes.NewReader(body))
		response.ContentLength = int64(len(body))
		close(claimCommitted)
		<-releaseResponse
		return response, nil
	})}

	startManager(t, manager)
	worker := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool {
		return worker.Health == "healthy"
	})
	task := createTask(t, fixture.store, worker, "health", "success", 60)
	select {
	case <-claimCommitted:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not commit a claim")
	}
	if err := os.Remove(codexPath); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		manager.stateMutex.Lock()
		defer manager.stateMutex.Unlock()
		return manager.health.State == "unhealthy"
	})
	close(releaseResponse)

	detail := waitForTaskState(t, fixture.store, task.Task.ID, "failed")
	if len(detail.Attempts) != 1 || detail.Attempts[0].State != "failed" ||
		!strings.Contains(detail.Attempts[0].Error, "ineligible") {
		t.Fatalf("committed ineligible claim was not failed precisely: %#v", detail.Attempts)
	}
	if _, err := os.Stat(filepath.Join(manager.dataDirectory, "worktrees", detail.Attempts[0].ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ineligible claim created a worktree: %v", err)
	}
}

func repositoryIdentity(t *testing.T, path string) string {
	t.Helper()
	repository, err := resolveRepository("test", path, "git")
	if err != nil {
		t.Fatal(err)
	}
	return repository.RemoteIdentity
}

func TestLostClaimAndCompletionResponsesAreIdempotent(t *testing.T) {
	var droppedClaim atomic.Bool
	var droppedCompletion atomic.Bool
	fixture := newServerFixture(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			isClaim := strings.HasSuffix(request.URL.Path, "/claims") && !droppedClaim.Load()
			isCompletion := strings.HasSuffix(request.URL.Path, "/complete") && !droppedCompletion.Load()
			if isClaim || isCompletion {
				recorder := httptest.NewRecorder()
				next.ServeHTTP(recorder, request)
				shouldDrop := recorder.Code == http.StatusOK &&
					((isClaim && droppedClaim.CompareAndSwap(false, true)) ||
						(isCompletion && droppedCompletion.CompareAndSwap(false, true)))
				if shouldDrop {
					hijacker, ok := writer.(http.Hijacker)
					if !ok {
						t.Error("test server cannot hijack connections")
						return
					}
					connection, _, err := hijacker.Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = connection.Close()
					return
				}
				copyResponse(writer, recorder)
				return
			}
			next.ServeHTTP(writer, request)
		})
	})
	repository := createRepository(t, "replay")
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"replay": repository}, 1)
	startManager(t, manager)
	worker := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool { return worker.Health == "healthy" })
	task := createTask(t, fixture.store, worker, "replay", "success", 60)
	detail := waitForTaskState(t, fixture.store, task.Task.ID, "succeeded")
	if !droppedClaim.Load() || !droppedCompletion.Load() || len(detail.Attempts) != 1 {
		t.Fatalf("dropped claim=%v completion=%v attempts=%d",
			droppedClaim.Load(), droppedCompletion.Load(), len(detail.Attempts))
	}
}

func TestCodexStartsOnlyAfterAttemptStartIsAccepted(t *testing.T) {
	startReached := make(chan struct{}, 1)
	releaseStart := make(chan struct{})
	fixture := newServerFixture(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if strings.HasSuffix(request.URL.Path, "/start") {
				select {
				case startReached <- struct{}{}:
				default:
				}
				select {
				case <-releaseStart:
				case <-request.Context().Done():
					return
				}
			}
			next.ServeHTTP(writer, request)
		})
	})
	repository := createRepository(t, "start-order")
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"start-order": repository}, 1)
	startManager(t, manager)
	worker := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool { return worker.Health == "healthy" })
	task := createTask(t, fixture.store, worker, "start-order", "success", 60)
	select {
	case <-startReached:
	case <-time.After(10 * time.Second):
		t.Fatal("worker never requested attempt start")
	}
	if entries, err := os.ReadDir(os.Getenv("FACTORY_TEST_CODEX_LOG")); err == nil && len(entries) != 0 {
		t.Fatalf("Codex started before the start request was accepted: %v", entries)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	close(releaseStart)
	waitForTaskState(t, fixture.store, task.Task.ID, "succeeded")
}

func copyResponse(writer http.ResponseWriter, recorder *httptest.ResponseRecorder) {
	for key, values := range recorder.Header() {
		for _, value := range values {
			writer.Header().Add(key, value)
		}
	}
	writer.WriteHeader(recorder.Code)
	_, _ = writer.Write(recorder.Body.Bytes())
}

func TestFailureRetainsWorktree(t *testing.T) {
	repository := createRepository(t, "failure")
	fixture := newServerFixture(t, nil)
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"failure": repository}, 1)
	startManager(t, manager)
	worker := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool { return worker.Health == "healthy" })
	task := createTask(t, fixture.store, worker, "failure", "fail", 60)
	detail := waitForTaskState(t, fixture.store, task.Task.ID, "failed")
	if len(detail.Attempts) != 1 ||
		!strings.Contains(detail.Attempts[0].Error, "exit status 17") ||
		!strings.Contains(detail.Attempts[0].Error, "deterministic failure") {
		t.Fatalf("failed attempt = %#v", detail.Attempts)
	}
	if _, err := os.Stat(filepath.Join(manager.dataDirectory, "worktrees", detail.Attempts[0].ID)); err != nil {
		t.Fatalf("failed worktree was not retained: %v", err)
	}
	branch := "factory/" + detail.Task.ID[:12] + "-" + detail.Attempts[0].ID[:12]
	runGitTest(t, repository.path, "show-ref", "--verify", "refs/heads/"+branch)
}

func TestDirtyAndUnpublishedSuccessesAreRetained(t *testing.T) {
	repository := createRepository(t, "unsafe-success")
	fixture := newServerFixture(t, nil)
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"unsafe-success": repository}, 2)
	startManager(t, manager)
	worker := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool { return worker.Health == "healthy" })
	dirty := createTask(t, fixture.store, worker, "unsafe-success", "dirty", 60)
	unpublished := createTask(t, fixture.store, worker, "unsafe-success", "unpublished", 60)
	dirty = waitForTaskState(t, fixture.store, dirty.Task.ID, "succeeded")
	unpublished = waitForTaskState(t, fixture.store, unpublished.Task.ID, "succeeded")
	for _, attempt := range []protocol.Attempt{dirty.Attempts[0], unpublished.Attempts[0]} {
		path := filepath.Join(manager.dataDirectory, "worktrees", attempt.ID)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unsafe successful worktree was removed: %v", err)
		}
		waitFor(t, 5*time.Second, func() bool {
			manager.stateMutex.Lock()
			defer manager.stateMutex.Unlock()
			_, retained := manager.retained[attempt.ID]
			return retained
		})
		taskID := dirty.Task.ID
		if attempt.ID == unpublished.Attempts[0].ID {
			taskID = unpublished.Task.ID
		}
		branch := "factory/" + taskID[:12] + "-" + attempt.ID[:12]
		runGitTest(t, repository.path, "show-ref", "--verify", "refs/heads/"+branch)
	}
}

func TestCancellationStopsCompleteProcessGroup(t *testing.T) {
	repository := createRepository(t, "cancel")
	fixture := newServerFixture(t, nil)
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"cancel": repository}, 1)
	startManager(t, manager)
	worker := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool { return worker.Health == "healthy" })
	task := createTask(t, fixture.store, worker, "cancel", "fork", 60)
	running := waitForTaskState(t, fixture.store, task.Task.ID, "running")
	attemptID := running.Attempts[0].ID
	childPath := filepath.Join(os.Getenv("FACTORY_TEST_CODEX_LOG"), attemptID+".child")
	waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(childPath)
		return err == nil
	})
	childPID := readPID(t, childPath)
	started := time.Now()
	if _, err := fixture.store.CancelTask(context.Background(), task.Task.ID); err != nil {
		t.Fatal(err)
	}
	detail := waitForTaskState(t, fixture.store, task.Task.ID, "cancelled")
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
	waitForProcessGone(t, childPID, 3*time.Second)
	if _, err := os.Stat(filepath.Join(manager.dataDirectory, "worktrees", detail.Attempts[0].ID)); err != nil {
		t.Fatalf("cancelled worktree was not retained: %v", err)
	}
	branch := "factory/" + detail.Task.ID[:12] + "-" + detail.Attempts[0].ID[:12]
	runGitTest(t, repository.path, "show-ref", "--verify", "refs/heads/"+branch)
}

func TestSuccessfulLeaderExitStopsDescendantHoldingOutputPipes(t *testing.T) {
	repository := createRepository(t, "leader-exit")
	fixture := newServerFixture(t, nil)
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"leader-exit": repository}, 1)
	startManager(t, manager)
	worker := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool {
		return worker.Health == "healthy"
	})
	task := createTask(t, fixture.store, worker, "leader-exit", "descendant", 60)
	detail := waitForTaskState(t, fixture.store, task.Task.ID, "succeeded")
	if len(detail.Attempts) != 1 || detail.Attempts[0].Result != "leader completed" {
		t.Fatalf("leader-exit attempt = %#v", detail.Attempts)
	}
	childPath := filepath.Join(os.Getenv("FACTORY_TEST_CODEX_LOG"), detail.Attempts[0].ID+".child")
	childPID := readPID(t, childPath)
	waitForProcessGone(t, childPID, 3*time.Second)
}

func TestTimeoutStopsIgnoringProcessGroup(t *testing.T) {
	repository := createRepository(t, "timeout")
	fixture := newServerFixture(t, nil)
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"timeout": repository}, 1)
	startManager(t, manager)
	worker := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool { return worker.Health == "healthy" })
	task := createTask(t, fixture.store, worker, "timeout", "fork", 1)
	running := waitForTaskState(t, fixture.store, task.Task.ID, "running")
	childPath := filepath.Join(os.Getenv("FACTORY_TEST_CODEX_LOG"), running.Attempts[0].ID+".child")
	waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(childPath)
		return err == nil
	})
	childPID := readPID(t, childPath)
	detail := waitForTaskState(t, fixture.store, task.Task.ID, "failed")
	if !strings.Contains(detail.Attempts[0].Error, "timeout") {
		t.Fatalf("timeout error = %q", detail.Attempts[0].Error)
	}
	waitForProcessGone(t, childPID, 3*time.Second)
}

func TestTimeoutIncludesWorktreePreparation(t *testing.T) {
	repository := createRepository(t, "preparation-timeout")
	fixture := newServerFixture(t, nil)
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gitWrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\ncase \"$*\" in *\"worktree add\"*) sleep 3;; esac\nexec " +
		strconv.Quote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(gitWrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"preparation-timeout": repository}, 1)
	manager.options.GitExecutable = gitWrapper
	startManager(t, manager)
	worker := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool { return worker.Health == "healthy" })
	task := createTask(t, fixture.store, worker, "preparation-timeout", "success", 1)
	detail := waitForTaskState(t, fixture.store, task.Task.ID, "failed")
	if len(detail.Attempts) != 1 || !strings.Contains(detail.Attempts[0].Error, "task timeout") {
		t.Fatalf("preparation timeout attempt = %#v", detail.Attempts)
	}
	if entries, err := os.ReadDir(os.Getenv("FACTORY_TEST_CODEX_LOG")); err == nil && len(entries) != 0 {
		t.Fatalf("Codex started after the task timed out during preparation: %v", entries)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestSupervisorCrashStopsRecordedProcessGroup(t *testing.T) {
	repository := createRepository(t, "supervisor-crash")
	fixture := newServerFixture(t, nil)
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"supervisor-crash": repository}, 1)
	startManager(t, manager)
	worker := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool { return worker.Health == "healthy" })
	task := createTask(t, fixture.store, worker, "supervisor-crash", "fork", 60)
	running := waitForTaskState(t, fixture.store, task.Task.ID, "running")
	attempt := running.Attempts[0]
	if attempt.SupervisorPID == nil {
		t.Fatal("control plane did not record the supervisor PID")
	}
	identity, err := processIdentity(int(*attempt.SupervisorPID))
	if err != nil {
		t.Fatal(err)
	}
	if identity != attempt.ProcessIdentity {
		t.Fatalf("stored supervisor identity does not match PID %d", *attempt.SupervisorPID)
	}
	group, err := processGroupID(int(*attempt.SupervisorPID))
	if err != nil {
		t.Fatal(err)
	}
	if group != int(*attempt.SupervisorPID) || group == unix.Getpgrp() {
		t.Fatalf("supervisor process group = %d; supervisor PID = %d; worker group = %d",
			group, *attempt.SupervisorPID, unix.Getpgrp())
	}
	childPath := filepath.Join(os.Getenv("FACTORY_TEST_CODEX_LOG"), attempt.ID+".child")
	waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(childPath)
		return err == nil
	})
	childPID := readPID(t, childPath)
	if err := unix.Kill(int(*attempt.SupervisorPID), unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitForTaskState(t, fixture.store, task.Task.ID, "failed")
	waitForProcessGone(t, childPID, 8*time.Second)
	waitFor(t, 5*time.Second, func() bool {
		manager.stateMutex.Lock()
		defer manager.stateMutex.Unlock()
		return manager.fatalHealth != nil && manager.health.State == "unhealthy"
	})
}

func TestServerLossStopsCodexBeforeLeaseExpiry(t *testing.T) {
	repository := createRepository(t, "server-loss")
	fixture := newServerFixture(t, nil)
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"server-loss": repository}, 1)
	startManager(t, manager)
	worker := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool { return worker.Health == "healthy" })
	task := createTask(t, fixture.store, worker, "server-loss", "fork", 60)
	running := waitForTaskState(t, fixture.store, task.Task.ID, "running")
	childPath := filepath.Join(os.Getenv("FACTORY_TEST_CODEX_LOG"), running.Attempts[0].ID+".child")
	waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(childPath)
		return err == nil
	})
	childPID := readPID(t, childPath)
	started := time.Now()
	fixture.server.Close()
	waitForProcessGone(t, childPID, 32*time.Second)
	if elapsed := time.Since(started); elapsed > protocol.LeaseDuration+2*time.Second {
		t.Fatalf("server-loss stop took %s", elapsed)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := fixture.store.SweepExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	detail := waitForTaskState(t, fixture.store, task.Task.ID, "failed")
	if detail.Attempts[0].State != "lost" {
		t.Fatalf("server-loss attempt state = %s", detail.Attempts[0].State)
	}
}

func TestGracefulShutdownStopsActiveGroup(t *testing.T) {
	repository := createRepository(t, "shutdown")
	fixture := newServerFixture(t, nil)
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"shutdown": repository}, 1)
	cancel, done := startManager(t, manager)
	worker := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool { return worker.Health == "healthy" })
	task := createTask(t, fixture.store, worker, "shutdown", "fork", 60)
	running := waitForTaskState(t, fixture.store, task.Task.ID, "running")
	queued := createTask(t, fixture.store, worker, "shutdown", "success", 60)
	childPath := filepath.Join(os.Getenv("FACTORY_TEST_CODEX_LOG"), running.Attempts[0].ID+".child")
	waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(childPath)
		return err == nil
	})
	childPID := readPID(t, childPath)
	started := time.Now()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Fatalf("graceful shutdown took %s", elapsed)
	}
	waitForProcessGone(t, childPID, 3*time.Second)
	detail := waitForTaskState(t, fixture.store, task.Task.ID, "cancelled")
	if detail.Attempts[0].State != "cancelled" {
		t.Fatalf("shutdown attempt state = %s", detail.Attempts[0].State)
	}
	queuedDetail, err := fixture.store.Task(context.Background(), queued.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queuedDetail.Execution.State != "queued" || len(queuedDetail.Attempts) != 0 {
		t.Fatalf("worker claimed new work during shutdown: %#v", queuedDetail)
	}
}

func TestParentPipeLossStopsCodexGroup(t *testing.T) {
	t.Setenv("FACTORY_TEST_SUPERVISOR", "1")
	logDirectory := t.TempDir()
	t.Setenv("FACTORY_TEST_CODEX_LOG", logDirectory)
	repository := createRepository(t, "parent-loss")
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	result := filepath.Join(t.TempDir(), "result")
	process, err := startSupervisor(
		[]string{os.Args[0], "-test.run=TestWorkerSupervisorHelperProcess", "--"},
		supervisorInit{
			RuntimeExecutable: codexPath, Worktree: repository.path, ResultPath: result,
			Prompt: "FAKE_MODE=fork", TimeoutSeconds: 60,
		}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.awaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := process.send("start"); err != nil {
		t.Fatal(err)
	}
	attempt := filepath.Base(repository.path)
	childPath := filepath.Join(logDirectory, attempt+".child")
	waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(childPath)
		return err == nil
	})
	childPID := readPID(t, childPath)
	started := time.Now()
	if err := process.closeControl(); err != nil {
		t.Fatal(err)
	}
	waitForProcessGone(t, childPID, 8*time.Second)
	if elapsed := time.Since(started); elapsed > 7*time.Second {
		t.Fatalf("parent pipe loss took %s", elapsed)
	}
}

func TestRetiredWorktreeAndStateIsolation(t *testing.T) {
	repository := createRepository(t, "isolation")
	v1Root := filepath.Join(t.TempDir(), ".factory", "v1-owned")
	runGitTest(t, repository.path, "worktree", "add", "-b", "codex/v1-owned", v1Root, "HEAD")
	marker := filepath.Join(v1Root, "v1-state.sqlite3")
	if err := os.WriteFile(marker, []byte("v1-state"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := newServerFixture(t, nil)
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), ".factory", "worker"),
		map[string]repositoryFixture{"isolation": repository}, 1)
	startManager(t, manager)
	worker := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool { return worker.Health == "healthy" })
	task := createTask(t, fixture.store, worker, "isolation", "success", 60)
	waitForTaskState(t, fixture.store, task.Task.ID, "succeeded")
	body, err := os.ReadFile(marker)
	if err != nil || string(body) != "v1-state" {
		t.Fatalf("retired state changed: %q, %v", body, err)
	}
	entries := runGitTest(t, repository.path, "worktree", "list", "--porcelain")
	if !strings.Contains(entries, v1Root) || !strings.Contains(entries, "refs/heads/codex/v1-owned") {
		t.Fatalf("retired worktree registration changed:\n%s", entries)
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func waitForProcessGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, func() bool {
		err := unix.Kill(pid, 0)
		return errors.Is(err, unix.ESRCH)
	})
}

func TestLoadConfigRejectsUnknownAndResolvesRelativePaths(t *testing.T) {
	root := t.TempDir()
	repository := createRepository(t, "config")
	configPath := filepath.Join(root, "worker.toml")
	body := fmt.Sprintf(`server = "http://127.0.0.1:7337"
name = "local"
runtime = "claude-code"
max_concurrent = 1
data_directory = "data"
source_access = ["github"]

[repositories.factory]
path = %q
base_branch = "release/2026.07"
`, repository.path)
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.DataDirectory != filepath.Join(root, "data") {
		t.Fatalf("resolved data directory = %s", config.DataDirectory)
	}
	if config.Runtime != protocol.RuntimeClaudeCode {
		t.Fatalf("runtime = %q", config.Runtime)
	}
	if config.Repositories["factory"].BaseBranch != "release/2026.07" {
		t.Fatalf("base branch = %q", config.Repositories["factory"].BaseBranch)
	}
	if !reflect.DeepEqual(config.SourceAccess, []string{"github"}) {
		t.Fatalf("source access = %#v", config.SourceAccess)
	}
	invalidSource := strings.Replace(body, `["github"]`, `["linear"]`, 1)
	if err := os.WriteFile(configPath, []byte(invalidSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(configPath); err == nil || !strings.Contains(err.Error(), "supports github") {
		t.Fatalf("unsupported source access error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte(body+"unsafe_shortcut = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(configPath); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown field error = %v", err)
	}
}

func TestLoadConfigDefaultsDataDirectoryFromConfigFilename(t *testing.T) {
	root := t.TempDir()
	body := []byte(`name = "local"
runtime = "codex"
`)

	for _, filename := range []string{"worker.toml", "claude-worker.toml"} {
		configPath := filepath.Join(root, filename)
		if err := os.WriteFile(configPath, body, 0o600); err != nil {
			t.Fatal(err)
		}
		config, err := LoadConfig(configPath)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(root, "workers", strings.TrimSuffix(filename, ".toml"))
		if config.DataDirectory != want {
			t.Fatalf("%s data directory = %q, want %q", filename, config.DataDirectory, want)
		}
	}
}

func TestLoadConfigDistinguishesOmittedAndEmptyDataDirectory(t *testing.T) {
	root := t.TempDir()

	t.Run("omitted", func(t *testing.T) {
		configPath := filepath.Join(root, "omitted.toml")
		if err := os.WriteFile(configPath, []byte(`name = "local"`), 0o600); err != nil {
			t.Fatal(err)
		}
		config, err := LoadConfig(configPath)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(root, "workers", "omitted")
		if config.DataDirectory != want {
			t.Fatalf("data directory = %q, want %q", config.DataDirectory, want)
		}
	})

	t.Run("explicitly empty", func(t *testing.T) {
		configPath := filepath.Join(root, "empty.toml")
		body := []byte("name = \"local\"\ndata_directory = \"\"\n")
		if err := os.WriteFile(configPath, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(configPath); err == nil || !strings.Contains(err.Error(), "data_directory is required") {
			t.Fatalf("explicitly empty data directory error = %v", err)
		}
	})
}

func TestLoadConfigPreservesExplicitDataDirectories(t *testing.T) {
	root := t.TempDir()
	absolute := filepath.Join(t.TempDir(), "worker-data")
	for name, dataDirectory := range map[string]string{
		"relative": "custom-data",
		"absolute": absolute,
	} {
		t.Run(name, func(t *testing.T) {
			configPath := filepath.Join(root, name+".toml")
			body := fmt.Sprintf("name = %q\ndata_directory = %q\n", name, dataDirectory)
			if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			config, err := LoadConfig(configPath)
			if err != nil {
				t.Fatal(err)
			}
			want := dataDirectory
			if !filepath.IsAbs(want) {
				want = filepath.Join(root, want)
			}
			if config.DataDirectory != want {
				t.Fatalf("data directory = %q, want %q", config.DataDirectory, want)
			}
		})
	}
}

func TestLoadConfigRejectsDefaultWithoutUsableBasename(t *testing.T) {
	for _, filename := range []string{".toml", "..toml", "...toml"} {
		t.Run(filename, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), filename)
			if err := os.WriteFile(configPath, []byte(`name = "local"`), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(configPath); err == nil || !strings.Contains(err.Error(), "no usable basename") {
				t.Fatalf("invalid default basename error = %v", err)
			}
		})
	}
}

func TestDerivedDataDirectoryReusesIdentityAndRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "worker.toml")
	if err := os.WriteFile(configPath, []byte(`name = "local"`), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ResolveWorkerID(config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveWorkerID(config)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("worker ID changed: %q != %q", first, second)
	}

	unsafeRoot := t.TempDir()
	unsafeConfigPath := filepath.Join(unsafeRoot, "unsafe.toml")
	if err := os.WriteFile(unsafeConfigPath, []byte(`name = "unsafe"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(unsafeRoot, "workers"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(unsafeRoot, "workers", "unsafe")); err != nil {
		t.Fatal(err)
	}
	unsafeConfig, err := LoadConfig(unsafeConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWorkerID(unsafeConfig); err == nil || !strings.Contains(err.Error(), "not a symlink") {
		t.Fatalf("derived symlink error = %v", err)
	}
}

func TestDefaultConfigPathUsesFactoryHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FACTORY_DATA_HOME", "")
	t.Setenv("FACTORY_WORKER_CONFIG", "")

	path, err := DefaultConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".factory", "worker.toml"); path != want {
		t.Fatalf("config path = %q, want %q", path, want)
	}
}

func TestDefaultConfigPathHonorsOverrides(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FACTORY_DATA_HOME", root)
	t.Setenv("FACTORY_WORKER_CONFIG", "")

	path, err := DefaultConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "worker.toml"); path != want {
		t.Fatalf("config path = %q, want %q", path, want)
	}

	explicit := filepath.Join(t.TempDir(), "custom.toml")
	t.Setenv("FACTORY_WORKER_CONFIG", explicit)
	path, err = DefaultConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if path != explicit {
		t.Fatalf("config path = %q, want %q", path, explicit)
	}
}

func TestDefaultConfigPathHonorsPreviewAliases(t *testing.T) {
	root := t.TempDir()
	explicit := filepath.Join(t.TempDir(), "preview.toml")
	t.Setenv("FACTORY_DATA_HOME", "")
	t.Setenv("FACTORY_WORKER_CONFIG", "")
	t.Setenv("FACTORY_V2_DATA_HOME", root)
	t.Setenv("FACTORY_V2_WORKER_CONFIG", "")

	path, err := DefaultConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "worker.toml"); path != want {
		t.Fatalf("config path = %q, want %q", path, want)
	}

	t.Setenv("FACTORY_V2_WORKER_CONFIG", explicit)
	path, err = DefaultConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if path != explicit {
		t.Fatalf("config path = %q, want %q", path, explicit)
	}
}

func TestValidateNoLegacyDefaultConfigRefusesLegacyState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FACTORY_DATA_HOME", "")
	t.Setenv("FACTORY_WORKER_CONFIG", "")
	legacyRoot := filepath.Join(home, ".factory-v2")
	legacyConfig := filepath.Join(legacyRoot, "worker.toml")
	if err := os.MkdirAll(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyConfig, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := ValidateNoLegacyDefaultConfig()
	if err == nil {
		t.Fatal("preview worker state was accepted")
	}
	for _, want := range []string{legacyConfig, "FACTORY_DATA_HOME=" + legacyRoot} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}

	t.Setenv("FACTORY_DATA_HOME", legacyRoot)
	if err := ValidateNoLegacyDefaultConfig(); err != nil {
		t.Fatalf("explicit legacy root validation: %v", err)
	}
	path, err := DefaultConfigPath()
	if err != nil {
		t.Fatalf("explicit legacy root: %v", err)
	}
	if path != legacyConfig {
		t.Fatalf("explicit legacy config = %q, want %q", path, legacyConfig)
	}

	t.Setenv("FACTORY_DATA_HOME", "")
	currentConfig := filepath.Join(home, ".factory", "worker.toml")
	if err := os.MkdirAll(filepath.Dir(currentConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(currentConfig, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNoLegacyDefaultConfig(); err != nil {
		t.Fatalf("preview state overrode current default: %v", err)
	}
}

func TestServerURLRejectsNonLoopback(t *testing.T) {
	for _, value := range []string{
		"http://0.0.0.0:7337",
		"http://example.com:7337",
		"https://127.0.0.1:7337",
		"http://127.0.0.1:7337/path",
	} {
		t.Run(value, func(t *testing.T) {
			if err := validateServerURL(value); err == nil {
				t.Fatalf("accepted %s", value)
			}
		})
	}
}

func TestWorkerRefusesRetiredDataRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "factory.sqlite3"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveDataDirectory(filepath.Join(root, "workers", "local")); err == nil ||
		!strings.Contains(err.Error(), "retired local state") {
		t.Fatalf("retired data-root error = %v", err)
	}

	linkRoot := t.TempDir()
	link := filepath.Join(linkRoot, "v1-link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveDataDirectory(filepath.Join(link, "workers", "linked")); err == nil ||
		!strings.Contains(err.Error(), "retired local state") {
		t.Fatalf("symlinked retired data-root error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "workers")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired directory was mutated before refusal: %v", err)
	}

	cleanTarget := t.TempDir()
	terminalLink := filepath.Join(t.TempDir(), "worker-link")
	if err := os.Symlink(cleanTarget, terminalLink); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveDataDirectory(terminalLink); err == nil ||
		!strings.Contains(err.Error(), "not a symlink") {
		t.Fatalf("terminal symlink error = %v", err)
	}
}

func TestProcessIdentityRefusesWrongOwner(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "trap '' TERM; while :; do sleep 1; done")
	configureNewProcessGroup(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	if err := stopOwnedProcessGroup(command.Process.Pid, "wrong identity", 0); err == nil {
		t.Fatal("signalled a process group with the wrong identity")
	}
	if err := unix.Kill(command.Process.Pid, 0); err != nil {
		t.Fatalf("wrong-identity check stopped process: %v", err)
	}
}

func TestEventPayloadIsAlwaysBoundedJSON(t *testing.T) {
	cases := map[string]string{
		"unicode":       strings.Repeat("é", protocol.MaxEventBytes),
		"escaped":       strings.Repeat("\"\\\n\t", protocol.MaxEventBytes),
		"control bytes": strings.Repeat("\x00\x01\x02", protocol.MaxEventBytes),
		"invalid UTF-8": string(bytes.Repeat([]byte{0xff, 0xfe}, protocol.MaxEventBytes)),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			payload := eventPayload("stderr", text, false)
			if len(payload) > protocol.MaxEventBytes || !jsonValid(payload) {
				t.Fatalf("payload length=%d valid=%v", len(payload), jsonValid(payload))
			}
		})
	}
}

func TestSupervisorOutputBoundsOneLogicalLineAndPreservesNextLine(t *testing.T) {
	first := strings.Repeat("x", maxSupervisorLineBytes+100)
	second := `{"type":"item.completed","text":"next"}`
	var output bytes.Buffer
	writer := &synchronizedEncoder{encoder: json.NewEncoder(&output)}
	streamSupervisorOutput(strings.NewReader(first+"\n"+second+"\n"), "stdout", writer, nil, nil)

	decoder := json.NewDecoder(&output)
	var messages []supervisorMessage
	for {
		var message supervisorMessage
		err := decoder.Decode(&message)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	if len(messages) != 2 {
		t.Fatalf("supervisor output message count = %d", len(messages))
	}
	if len(messages[0].Text) != maxSupervisorLineBytes || !messages[0].Truncated {
		t.Fatalf("oversized message length = %d; truncated = %v",
			len(messages[0].Text), messages[0].Truncated)
	}
	if messages[1].Text != second || messages[1].Truncated {
		t.Fatalf("following message = %#v", messages[1])
	}
}

func TestClaudeResultCaptureRejectsOversizedMalformedAndNonResultLines(t *testing.T) {
	cases := map[string]string{
		"malformed result": `{"type":"result","result":"` +
			strings.Repeat("x", maxSupervisorLineBytes+100) + `\q"}`,
		"non-result event": `{"type":"assistant","result":"` +
			strings.Repeat("x", maxSupervisorLineBytes+100) + `"}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			capture := &claudeResultCapture{}
			writer := &synchronizedEncoder{encoder: json.NewEncoder(io.Discard)}
			streamSupervisorOutput(strings.NewReader(input+"\n"), "stdout", writer, nil, capture.capture)
			if capture.found {
				t.Fatalf("oversized %s was accepted as a terminal result", name)
			}
		})
	}
}

func TestClaudeResultCaptureDecodesEscapedResultBeyondOutputLimit(t *testing.T) {
	result := strings.Repeat("\x00", maxSupervisorLineBytes/6+100)
	input, err := json.Marshal(map[string]any{
		"type":     "result",
		"subtype":  "success",
		"is_error": false,
		"result":   result,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(input) <= maxSupervisorLineBytes || len(result) >= protocol.MaxResultBytes {
		t.Fatalf("invalid test bounds: encoded=%d result=%d", len(input), len(result))
	}
	capture := &claudeResultCapture{}
	writer := &synchronizedEncoder{encoder: json.NewEncoder(io.Discard)}
	streamSupervisorOutput(bytes.NewReader(append(input, '\n')), "stdout", writer, nil, capture.capture)
	if !capture.found || capture.truncated || capture.result != result {
		t.Fatalf("escaped oversized-line result: found=%v truncated=%v result length=%d",
			capture.found, capture.truncated, len(capture.result))
	}
}

func TestEventSenderRetriesTransientFailureWithSameSequence(t *testing.T) {
	requests := make(chan protocol.EventBatchRequest, 2)
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var input protocol.EventBatchRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode event request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		requests <- input
		if count.Add(1) == 1 {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender := newEventSender(ctx, newClient(server.URL, nil), "attempt", "lease", protocol.RuntimeClaudeCode)
	sender.enqueue("stderr", "retry me", false)
	sender.closeAndWait(5 * time.Second)
	if count.Load() != 2 {
		t.Fatalf("event request count = %d", count.Load())
	}
	first := <-requests
	second := <-requests
	if len(first.Events) != 1 || len(second.Events) != 1 ||
		first.Events[0].Sequence != 0 || second.Events[0].Sequence != 0 ||
		first.Events[0].Kind != protocol.RuntimeClaudeCode ||
		second.Events[0].Kind != protocol.RuntimeClaudeCode ||
		!bytes.Equal(first.Events[0].Payload, second.Events[0].Payload) {
		t.Fatalf("event retry changed content: first=%#v second=%#v", first.Events, second.Events)
	}
}

func TestBoundedCompletionRequestFitsAfterJSONEscaping(t *testing.T) {
	cases := map[string]protocol.CompleteAttemptRequest{
		"control result": {
			LeaseToken: strings.Repeat("l", 43),
			State:      "succeeded",
			Result:     strings.Repeat("\x00", protocol.MaxResultBytes),
		},
		"invalid UTF-8 result": {
			LeaseToken: strings.Repeat("l", 43),
			State:      "succeeded",
			Result:     string(bytes.Repeat([]byte{0xff, 0xfe}, protocol.MaxResultBytes)),
		},
		"escaped result and invalid error": {
			LeaseToken: strings.Repeat("l", 43),
			State:      "failed",
			Result:     strings.Repeat("\"\\\n", protocol.MaxResultBytes),
			Error:      string(bytes.Repeat([]byte{0xff}, protocol.MaxErrorBytes)),
		},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			bounded := boundedCompletionRequest(input)
			body, err := json.Marshal(bounded)
			if err != nil {
				t.Fatal(err)
			}
			if len(body) > protocol.MaxBodyBytes {
				t.Fatalf("encoded completion length = %d", len(body))
			}
			if len(bounded.Result) > protocol.MaxResultBytes || len(bounded.Error) > protocol.MaxErrorBytes {
				t.Fatalf("raw bounds exceeded: result=%d error=%d", len(bounded.Result), len(bounded.Error))
			}
			if !utf8.ValidString(bounded.Result) || !utf8.ValidString(bounded.Error) {
				t.Fatal("bounded completion retained invalid UTF-8")
			}
		})
	}
}

func jsonValid(value []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(value))
	var decoded any
	return decoder.Decode(&decoded) == nil
}

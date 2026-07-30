package worker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const gitCommandTimeout = 30 * time.Second

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type worktree struct {
	Path       string
	Branch     string
	BaseCommit string
	HeadCommit string
}

func resolveRepositories(config Config, gitExecutable string) ([]Repository, error) {
	keys := make([]string, 0, len(config.Repositories))
	for key := range config.Repositories {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	repositories := make([]Repository, 0, len(keys))
	paths := make(map[string]string)
	identities := make(map[string]string)
	for _, key := range keys {
		repository, err := resolveRepository(key, config.Repositories[key].Path, gitExecutable)
		if err != nil {
			return nil, fmt.Errorf("repository %q: %w", key, err)
		}
		if previous := paths[repository.Path]; previous != "" {
			return nil, fmt.Errorf("repositories %q and %q resolve to the same path", previous, key)
		}
		if previous := identities[repository.RemoteIdentity]; previous != "" {
			return nil, fmt.Errorf("repositories %q and %q resolve to the same remote", previous, key)
		}
		paths[repository.Path] = key
		identities[repository.RemoteIdentity] = key
		repositories = append(repositories, repository)
	}
	return repositories, nil
}

func resolveRepository(key, path, gitExecutable string) (Repository, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Repository{}, fmt.Errorf("resolve path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return Repository{}, fmt.Errorf("canonicalize path: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return Repository{}, fmt.Errorf("inspect path: %w", err)
	}
	if !info.IsDir() {
		return Repository{}, errors.New("path is not a directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	stdout, stderr, commandErr := runCommand(ctx, gitExecutable, canonical, 64<<10, "rev-parse", "--is-inside-work-tree")
	cancel()
	if commandErr == nil && strings.TrimSpace(string(stdout)) != "true" {
		commandErr = errors.New("not a Git worktree")
	}
	var remote string
	if commandErr == nil {
		ctx, cancel = context.WithTimeout(context.Background(), gitCommandTimeout)
		stdout, stderr, commandErr = runCommand(ctx, gitExecutable, canonical, 64<<10, "remote", "get-url", "origin")
		cancel()
		if commandErr == nil {
			remote = strings.TrimSpace(string(stdout))
		}
	}
	if commandErr != nil {
		if !executableUnavailable(commandErr) {
			return Repository{}, commandFailure("verify Git repository", stdout, stderr, commandErr)
		}
		remote, err = readOriginRemote(canonical)
		if err != nil {
			return Repository{}, commandFailure("verify Git repository", stdout, stderr, commandErr)
		}
	}
	identity, err := normalizeRemoteIdentity(remote, canonical)
	if err != nil {
		return Repository{}, err
	}
	return Repository{Key: key, Path: canonical, RemoteIdentity: identity}, nil
}

func executableUnavailable(err error) bool {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
		return true
	}
	var executableError *exec.Error
	return errors.As(err, &executableError) && errors.Is(executableError.Err, exec.ErrNotFound)
}

func readOriginRemote(repository string) (string, error) {
	gitPath := filepath.Join(repository, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return "", err
	}
	var configPath string
	if info.IsDir() {
		configPath = filepath.Join(gitPath, "config")
	} else if info.Mode().IsRegular() {
		body, err := os.ReadFile(gitPath)
		if err != nil {
			return "", err
		}
		line := strings.TrimSpace(string(body))
		if !strings.HasPrefix(line, "gitdir: ") {
			return "", errors.New(".git file has no gitdir")
		}
		gitDirectory := strings.TrimSpace(strings.TrimPrefix(line, "gitdir: "))
		if !filepath.IsAbs(gitDirectory) {
			gitDirectory = filepath.Join(repository, gitDirectory)
		}
		gitDirectory, err = filepath.Abs(gitDirectory)
		if err != nil {
			return "", err
		}
		commonDirectory := gitDirectory
		if common, readErr := os.ReadFile(filepath.Join(gitDirectory, "commondir")); readErr == nil {
			commonDirectory = filepath.Join(gitDirectory, strings.TrimSpace(string(common)))
		}
		configPath = filepath.Join(commonDirectory, "config")
	} else {
		return "", errors.New(".git is neither a directory nor a gitdir file")
	}
	file, err := os.Open(configPath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	inOrigin := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			inOrigin = line == `[remote "origin"]`
			continue
		}
		if inOrigin {
			key, value, found := strings.Cut(line, "=")
			if found && strings.TrimSpace(key) == "url" {
				remote := strings.TrimSpace(value)
				if remote != "" {
					return remote, nil
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", errors.New("origin remote is required")
}

func normalizeRemoteIdentity(remote, repositoryPath string) (string, error) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", errors.New("origin remote is empty")
	}
	if prefix, path, found := strings.Cut(remote, ":"); found && strings.Contains(prefix, "@") {
		at := strings.LastIndex(prefix, "@")
		host := prefix[at+1:]
		if host == "" || path == "" {
			return "", errors.New("origin SSH remote is malformed")
		}
		return normalizeHostPath(host, path), nil
	}
	parsed, err := url.Parse(remote)
	if err == nil && parsed.Scheme != "" {
		if parsed.Scheme == "file" {
			path, err := filepath.EvalSymlinks(parsed.Path)
			if err != nil {
				return "", fmt.Errorf("canonicalize file remote: %w", err)
			}
			return "file://" + filepath.ToSlash(path), nil
		}
		if parsed.Hostname() == "" || parsed.Path == "" {
			return "", errors.New("origin remote URL is malformed")
		}
		return normalizeHostPath(parsed.Host, parsed.Path), nil
	}
	path := remote
	if !filepath.IsAbs(path) {
		path = filepath.Join(repositoryPath, path)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("canonicalize origin path: %w", err)
	}
	return "file://" + filepath.ToSlash(canonical), nil
}

func normalizeHostPath(host, path string) string {
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	return strings.ToLower(host) + "/" + path
}

func createWorktree(ctx context.Context, gitExecutable, root string, repository Repository, taskID, attemptID string) (worktree, error) {
	value, err := prepareWorktree(ctx, gitExecutable, root, repository, taskID, attemptID)
	if err != nil {
		return value, err
	}
	if err := addPreparedWorktree(ctx, gitExecutable, repository, value); err != nil {
		return value, err
	}
	return value, nil
}

func prepareWorktree(ctx context.Context, gitExecutable, root string, repository Repository, taskID, attemptID string) (worktree, error) {
	if !uuidPattern.MatchString(taskID) || !uuidPattern.MatchString(attemptID) {
		return worktree{}, errors.New("server returned an invalid task or attempt ID")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return worktree{}, fmt.Errorf("create worktree root: %w", err)
	}
	if info, err := os.Lstat(root); err != nil {
		return worktree{}, fmt.Errorf("inspect worktree root: %w", err)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return worktree{}, errors.New("worktree root must be a real directory, not a symlink")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return worktree{}, fmt.Errorf("canonicalize worktree root: %w", err)
	}
	path := filepath.Join(root, attemptID)
	if _, err := os.Lstat(path); err == nil {
		return worktree{}, errors.New("attempt worktree path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return worktree{}, fmt.Errorf("inspect attempt worktree path: %w", err)
	}
	stdout, stderr, err := runGitCommand(ctx, gitExecutable, repository.Path, 64<<10, "rev-parse", "HEAD")
	if err != nil {
		return worktree{}, commandFailure("resolve repository HEAD", stdout, stderr, err)
	}
	base := strings.TrimSpace(string(stdout))
	if !commitPattern.MatchString(base) {
		return worktree{}, errors.New("repository HEAD is not a full commit ID")
	}
	branch := "factory/" + taskID[:12] + "-" + attemptID[:12]
	value := worktree{Path: path, Branch: branch, BaseCommit: base, HeadCommit: base}
	return value, nil
}

func addPreparedWorktree(ctx context.Context, gitExecutable string, repository Repository, value worktree) error {
	stdout, stderr, err := runGitCommand(ctx, gitExecutable, repository.Path, 256<<10,
		"worktree", "add", "-b", value.Branch, value.Path, value.BaseCommit)
	if err != nil {
		return commandFailure("create managed worktree", stdout, stderr, err)
	}
	return nil
}

type gitWorktreeEntry struct {
	Path   string
	Head   string
	Branch string
}

func listGitWorktrees(ctx context.Context, gitExecutable, repository string) ([]gitWorktreeEntry, error) {
	stdout, stderr, err := runGitCommand(ctx, gitExecutable, repository, 1<<20, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, commandFailure("list Git worktrees", stdout, stderr, err)
	}
	var entries []gitWorktreeEntry
	var current gitWorktreeEntry
	for _, token := range strings.Split(string(stdout), "\x00") {
		if token == "" {
			if current.Path != "" {
				entries = append(entries, current)
				current = gitWorktreeEntry{}
			}
			continue
		}
		key, value, _ := strings.Cut(token, " ")
		switch key {
		case "worktree":
			if current.Path != "" {
				entries = append(entries, current)
				current = gitWorktreeEntry{}
			}
			current.Path = value
		case "HEAD":
			current.Head = value
		case "branch":
			current.Branch = strings.TrimPrefix(value, "refs/heads/")
		}
	}
	if current.Path != "" {
		entries = append(entries, current)
	}
	return entries, nil
}

func cleanupSuccessfulWorktree(ctx context.Context, gitExecutable, root string, repository Repository, value worktree) error {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("canonicalize worktree root: %w", err)
	}
	path, err := filepath.EvalSymlinks(value.Path)
	if err != nil {
		return fmt.Errorf("canonicalize worktree path: %w", err)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("worktree is outside the Factory worktree root")
	}
	if strings.Contains(relative, string(filepath.Separator)) {
		return errors.New("worktree is not a direct child of the Factory worktree root")
	}
	stdout, stderr, err := runGitCommand(ctx, gitExecutable, path, 1<<20, "status", "--porcelain=v1", "-z")
	if err != nil {
		return commandFailure("inspect worktree status", stdout, stderr, err)
	}
	if len(stdout) != 0 {
		return errors.New("worktree is dirty")
	}
	stdout, stderr, err = runGitCommand(ctx, gitExecutable, path, 64<<10, "rev-parse", "HEAD")
	if err != nil {
		return commandFailure("resolve worktree HEAD", stdout, stderr, err)
	}
	value.HeadCommit = strings.TrimSpace(string(stdout))
	if !commitPattern.MatchString(value.HeadCommit) {
		return errors.New("worktree HEAD is not a full commit ID")
	}
	if value.HeadCommit != value.BaseCommit {
		stdout, stderr, err = runGitCommand(ctx, gitExecutable, path, 256<<10,
			"for-each-ref", "--format=%(refname)", "--contains", value.HeadCommit, "refs/remotes")
		if err != nil {
			return commandFailure("inspect published refs", stdout, stderr, err)
		}
		if strings.TrimSpace(string(stdout)) == "" {
			return errors.New("worktree contains unpublished commits")
		}
	}
	entries, err := listGitWorktrees(ctx, gitExecutable, repository.Path)
	if err != nil {
		return err
	}
	matches := 0
	for _, entry := range entries {
		entryPath, err := filepath.EvalSymlinks(entry.Path)
		if err != nil {
			continue
		}
		if entryPath == path {
			matches++
			if entry.Branch != value.Branch || entry.Head != value.HeadCommit {
				return errors.New("Git worktree identity does not match the live attempt")
			}
		}
	}
	if matches != 1 {
		return fmt.Errorf("expected one registered Git worktree, found %d", matches)
	}
	stdout, stderr, err = runGitCommand(ctx, gitExecutable, repository.Path, 256<<10, "worktree", "remove", path)
	if err != nil {
		return commandFailure("remove successful worktree", stdout, stderr, err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("Git reported cleanup success but the worktree path remains")
	}
	entries, err = listGitWorktrees(ctx, gitExecutable, repository.Path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		entryPath, err := filepath.Abs(entry.Path)
		if err == nil && entryPath == path {
			return errors.New("Git reported cleanup success but the worktree registration remains")
		}
	}
	return nil
}

func runGitCommand(
	ctx context.Context,
	gitExecutable string,
	directory string,
	outputLimit int,
	arguments ...string,
) ([]byte, []byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()
	return runCommand(commandContext, gitExecutable, directory, outputLimit, arguments...)
}

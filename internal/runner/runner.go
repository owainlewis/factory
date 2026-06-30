package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/agent"
	"github.com/owainlewis/factory/internal/audit"
	"github.com/owainlewis/factory/internal/config"
	"github.com/owainlewis/factory/internal/gitrepo"
	"github.com/owainlewis/factory/internal/labels"
	"github.com/owainlewis/factory/internal/prompt"
	"github.com/owainlewis/factory/internal/workflows"
)

type App struct {
	cfg config.Config
}

type Mode string

const (
	ModePlan    Mode = "plan"
	ModeExecute Mode = "execute"
)

func ParseMode(value string) (Mode, error) {
	switch value {
	case "", string(ModePlan):
		return ModePlan, nil
	case string(ModeExecute):
		return ModeExecute, nil
	default:
		return "", fmt.Errorf("unsupported mode %q", value)
	}
}

type RunRecord struct {
	ID         string    `json:"id"`
	Repo       string    `json:"repo"`
	RepoPath   string    `json:"repo_path"`
	Workflow   string    `json:"workflow"`
	Source     string    `json:"source"`
	Mode       Mode      `json:"mode"`
	Agent      string    `json:"agent"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	LogPath    string    `json:"log_path"`
	RecordPath string    `json:"record_path"`
	Blocker    string    `json:"blocker,omitempty"`
	Error      string    `json:"error,omitempty"`
}

func New(configPath string) (*App, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	return &App{cfg: cfg}, nil
}

func (a *App) ListRepos(w io.Writer) error {
	names := make([]string, 0, len(a.cfg.Repos))
	for name := range a.cfg.Repos {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		repo := a.cfg.Repos[name]
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, repo.Agent, repo.URL)
	}
	return nil
}

func (a *App) ListWorkflows(ctx context.Context, w io.Writer, repoName string) error {
	repoPath, unlock, err := a.ensureRepoPath(ctx, repoName)
	if err != nil {
		return err
	}
	defer unlock()

	discovered, err := workflows.Discover(repoPath)
	if err != nil {
		return err
	}

	for _, workflow := range discovered {
		state := "missing"
		if workflow.Runnable {
			state = "runnable"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", workflow.Name, workflow.Path, state)
	}
	return nil
}

// SyncLabels ensures the standard Factory labels exist on the GitHub repo
// behind repoName. Auth or permission failures are reported as blocked.
func (a *App) SyncLabels(ctx context.Context, w io.Writer, repoName string) error {
	repo, ok := a.cfg.Repos[repoName]
	if !ok {
		return fmt.Errorf("unknown repo %q", repoName)
	}
	if repo.URL == "" {
		return fmt.Errorf("repo %q has no GitHub url to sync labels against", repoName)
	}
	slug, err := labels.RepoSlug(repo.URL)
	if err != nil {
		return err
	}

	report, err := labels.Sync(ctx, labels.GHClient{Repo: slug})
	if err != nil {
		if labels.IsBlocked(err) {
			fmt.Fprintf(w, "blocked\t%s\t%v\n", slug, err)
		}
		return err
	}

	for _, name := range report.Created {
		fmt.Fprintf(w, "created\t%s\n", name)
	}
	for _, name := range report.Updated {
		fmt.Fprintf(w, "updated\t%s\n", name)
	}
	for _, name := range report.Unchanged {
		fmt.Fprintf(w, "ok\t%s\n", name)
	}
	return nil
}

func (a *App) Audit(ctx context.Context, w io.Writer, repoName string) error {
	repoPath, unlock, err := a.ensureRepoPath(ctx, repoName)
	if err != nil {
		return err
	}
	defer unlock()

	report, err := audit.Run(repoPath)
	if err != nil {
		return err
	}
	return audit.WriteMarkdown(w, repoName, report)
}

func (a *App) ListRuns(w io.Writer) error {
	dir := filepath.Join(a.cfg.Factory.DataDir, "runs")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		fmt.Fprintln(w, filepath.Join(dir, entry.Name()))
	}
	return nil
}

func (a *App) Run(ctx context.Context, repoName string, workflow string, mode Mode) (RunRecord, error) {
	repo, ok := a.cfg.Repos[repoName]
	if !ok {
		return RunRecord{}, fmt.Errorf("unknown repo %q", repoName)
	}
	parsedMode, err := ParseMode(string(mode))
	if err != nil {
		return RunRecord{}, err
	}
	mode = parsedMode
	repoPath := config.RepoPath(a.cfg.Factory.DataDir, repoName, repo)

	started := time.Now().UTC()
	runID := fmt.Sprintf("%s-%s-%s", started.Format("20060102T150405Z"), repoName, workflow)
	logPath := filepath.Join(a.cfg.Factory.DataDir, "logs", runID+".log")
	recordPath := filepath.Join(a.cfg.Factory.DataDir, "runs", runID+".json")

	record := RunRecord{
		ID:         runID,
		Repo:       repoName,
		RepoPath:   repoPath,
		Workflow:   workflow,
		Mode:       mode,
		Agent:      repo.Agent,
		Status:     "running",
		StartedAt:  started,
		LogPath:    logPath,
		RecordPath: recordPath,
	}

	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return record, err
	}
	if err := os.MkdirAll(filepath.Dir(recordPath), 0o755); err != nil {
		return record, err
	}

	lock, err := a.acquireRepoLock(ctx, repoName)
	if err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		record.FinishedAt = time.Now().UTC()
		_ = writeRecord(record)
		return record, err
	}
	defer lock.Release()

	if err := gitrepo.Ensure(ctx, repoPath, repo.URL, repo.Branch); err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		record.FinishedAt = time.Now().UTC()
		_ = writeRecord(record)
		return record, err
	}

	runRepoPath := repoPath
	if mode == ModeExecute {
		runRepoPath = filepath.Join(a.cfg.Factory.DataDir, "worktrees", repoName, runID)
		if err := gitrepo.AddWorktree(ctx, repoPath, runRepoPath, repo.Branch); err != nil {
			record.Status = "failed"
			record.Error = err.Error()
			record.FinishedAt = time.Now().UTC()
			_ = writeRecord(record)
			return record, err
		}
		record.RepoPath = runRepoPath
	}

	source, promptText, err := prompt.Build(runRepoPath, workflow, string(mode))
	if err != nil {
		record.Status = "blocked"
		record.Error = err.Error()
		record.FinishedAt = time.Now().UTC()
		_ = writeRecord(record)
		return record, err
	}
	record.Source = source

	adapter, err := adapterFor(repo.Agent)
	if err != nil {
		record.Status = "blocked"
		record.Error = err.Error()
		record.FinishedAt = time.Now().UTC()
		_ = writeRecord(record)
		return record, err
	}

	result, err := adapter.Run(ctx, agent.RunSpec{
		RepoPath: runRepoPath,
		Prompt:   promptText,
		LogPath:  logPath,
		Mode:     string(mode),
	})
	record.Status = result.Status
	record.Blocker = result.Blocker
	record.FinishedAt = time.Now().UTC()
	if err != nil {
		record.Error = err.Error()
		_ = writeRecord(record)
		return record, err
	}
	if record.Status == "" {
		record.Status = "success"
	}

	if err := writeRecord(record); err != nil {
		return record, err
	}
	return record, nil
}

func (a *App) ensureRepoPath(ctx context.Context, repoName string) (string, func(), error) {
	repo, ok := a.cfg.Repos[repoName]
	if !ok {
		return "", nil, fmt.Errorf("unknown repo %q", repoName)
	}

	repoPath := config.RepoPath(a.cfg.Factory.DataDir, repoName, repo)
	lock, err := a.acquireRepoLock(ctx, repoName)
	if err != nil {
		return "", nil, err
	}
	unlock := func() {
		_ = lock.Release()
	}
	if err := gitrepo.Ensure(ctx, repoPath, repo.URL, repo.Branch); err != nil {
		unlock()
		return "", nil, err
	}
	return repoPath, unlock, nil
}

func adapterFor(name string) (agent.Adapter, error) {
	switch name {
	case "", "claude":
		return agent.Claude{}, nil
	default:
		return nil, fmt.Errorf("unsupported agent %q", name)
	}
}

func writeRecord(record RunRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(record.RecordPath, append(data, '\n'), 0o644)
}

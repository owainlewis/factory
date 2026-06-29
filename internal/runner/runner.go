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
	"github.com/owainlewis/factory/internal/config"
	"github.com/owainlewis/factory/internal/gitrepo"
	"github.com/owainlewis/factory/internal/prompt"
)

type App struct {
	cfg config.Config
}

type RunRecord struct {
	ID         string    `json:"id"`
	Repo       string    `json:"repo"`
	RepoPath   string    `json:"repo_path"`
	Goal       string    `json:"goal"`
	GoalSource string    `json:"goal_source"`
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

func (a *App) Run(ctx context.Context, repoName string, goal string) (RunRecord, error) {
	repo, ok := a.cfg.Repos[repoName]
	if !ok {
		return RunRecord{}, fmt.Errorf("unknown repo %q", repoName)
	}

	repoPath := config.RepoPath(a.cfg.Factory.DataDir, repoName, repo)
	started := time.Now().UTC()
	runID := fmt.Sprintf("%s-%s-%s", started.Format("20060102T150405Z"), repoName, goal)
	logPath := filepath.Join(a.cfg.Factory.DataDir, "logs", runID+".log")
	recordPath := filepath.Join(a.cfg.Factory.DataDir, "runs", runID+".json")

	record := RunRecord{
		ID:         runID,
		Repo:       repoName,
		RepoPath:   repoPath,
		Goal:       goal,
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
	if err := gitrepo.Ensure(ctx, repoPath, repo.URL, repo.Branch); err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		record.FinishedAt = time.Now().UTC()
		_ = writeRecord(record)
		return record, err
	}

	goalSource, promptText, err := prompt.Build(repoPath, goal)
	if err != nil {
		record.Status = "blocked"
		record.Error = err.Error()
		record.FinishedAt = time.Now().UTC()
		_ = writeRecord(record)
		return record, err
	}
	record.GoalSource = goalSource

	adapter, err := adapterFor(repo.Agent)
	if err != nil {
		record.Status = "blocked"
		record.Error = err.Error()
		record.FinishedAt = time.Now().UTC()
		_ = writeRecord(record)
		return record, err
	}

	result, err := adapter.Run(ctx, agent.RunSpec{
		RepoPath: repoPath,
		Prompt:   promptText,
		LogPath:  logPath,
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

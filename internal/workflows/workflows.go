package workflows

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Workflow struct {
	Name     string
	Path     string
	Runnable bool
}

func Discover(repoPath string) ([]Workflow, error) {
	result := []Workflow{{
		Name:     "hello",
		Path:     "built-in:hello",
		Runnable: true,
	}}

	workflowsDir := preferredWorkflowsDir(repoPath)
	entries, err := os.ReadDir(workflowsDir)
	if err != nil {
		if os.IsNotExist(err) {
			result = append(result, Workflow{
				Name:     "repo-owned",
				Path:     workflowsDir,
				Runnable: false,
			})
			return result, nil
		}
		return nil, fmt.Errorf("read workflows directory: %w", err)
	}

	repoWorkflows := make([]Workflow, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		path := filepath.Join(workflowsDir, entry.Name())
		repoWorkflows = append(repoWorkflows, Workflow{
			Name:     strings.TrimSuffix(entry.Name(), ".md"),
			Path:     path,
			Runnable: true,
		})
	}

	sort.Slice(repoWorkflows, func(i, j int) bool {
		return repoWorkflows[i].Name < repoWorkflows[j].Name
	})
	result = append(result, repoWorkflows...)
	return result, nil
}

func preferredWorkflowsDir(repoPath string) string {
	factoryDir := filepath.Join(repoPath, ".factory", "WORKFLOWS")
	if info, err := os.Stat(factoryDir); err == nil && info.IsDir() {
		return factoryDir
	}
	return filepath.Join(repoPath, "WORKFLOWS")
}

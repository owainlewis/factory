package goals

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Goal struct {
	Name     string
	Path     string
	Runnable bool
}

func Discover(repoPath string) ([]Goal, error) {
	result := []Goal{{
		Name:     "hello",
		Path:     "built-in:hello",
		Runnable: true,
	}}

	goalsDir := filepath.Join(repoPath, ".factory", "goals")
	entries, err := os.ReadDir(goalsDir)
	if err != nil {
		if os.IsNotExist(err) {
			result = append(result, Goal{
				Name:     "repo-owned",
				Path:     goalsDir,
				Runnable: false,
			})
			return result, nil
		}
		return nil, fmt.Errorf("read goals directory: %w", err)
	}

	repoGoals := make([]Goal, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		path := filepath.Join(goalsDir, entry.Name())
		repoGoals = append(repoGoals, Goal{
			Name:     strings.TrimSuffix(entry.Name(), ".md"),
			Path:     path,
			Runnable: true,
		})
	}

	sort.Slice(repoGoals, func(i, j int) bool {
		return repoGoals[i].Name < repoGoals[j].Name
	})
	result = append(result, repoGoals...)
	return result, nil
}

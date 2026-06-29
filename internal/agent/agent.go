package agent

import "context"

type RunSpec struct {
	RepoPath string
	Prompt   string
	LogPath  string
	Mode     string
}

type RunResult struct {
	Status  string
	Output  string
	Blocker string
}

type Adapter interface {
	Run(context.Context, RunSpec) (RunResult, error)
}

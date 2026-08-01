package controlplane

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestGitHubCLIDiagnosticIsRequiredOnlyForProviderAutomations(t *testing.T) {
	store := newTestStore(t)
	diagnostic, err := store.DiagnoseGitHubCLI(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if diagnostic.Required {
		t.Fatalf("diagnostic = %#v, want provider not required", diagnostic)
	}

	store, _ = createAutomationFixture(t, false)
	originalLookPath := githubCLILookPath
	originalAuthStatus := githubCLIAuthStatus
	t.Cleanup(func() {
		githubCLILookPath = originalLookPath
		githubCLIAuthStatus = originalAuthStatus
	})

	githubCLILookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	diagnostic, err = store.DiagnoseGitHubCLI(context.Background())
	if err != nil || diagnostic.Code != "gh_missing" || diagnostic.Installed || diagnostic.Authenticated {
		t.Fatalf("missing gh diagnostic = %#v, error %v", diagnostic, err)
	}
	if !strings.Contains(diagnostic.Message, "Install gh") || !strings.Contains(diagnostic.Message, "gh auth login") {
		t.Fatalf("missing gh message is not actionable: %q", diagnostic.Message)
	}

	githubCLILookPath = func(string) (string, error) { return "/usr/bin/gh", nil }
	githubCLIAuthStatus = func(context.Context) ([]byte, error) {
		return []byte("not logged into github.com"), errors.New("exit status 1")
	}
	diagnostic, err = store.DiagnoseGitHubCLI(context.Background())
	if err != nil || diagnostic.Code != "gh_unauthenticated" || !diagnostic.Installed || diagnostic.Authenticated {
		t.Fatalf("unauthenticated gh diagnostic = %#v, error %v", diagnostic, err)
	}
	if !strings.Contains(diagnostic.Message, "not logged into github.com") || !strings.Contains(diagnostic.Message, "gh auth status") {
		t.Fatalf("unauthenticated gh message is not actionable: %q", diagnostic.Message)
	}

	githubCLIAuthStatus = func(context.Context) ([]byte, error) { return nil, nil }
	diagnostic, err = store.DiagnoseGitHubCLI(context.Background())
	if err != nil || diagnostic.Code != "" || !diagnostic.Installed || !diagnostic.Authenticated {
		t.Fatalf("ready gh diagnostic = %#v, error %v", diagnostic, err)
	}
}

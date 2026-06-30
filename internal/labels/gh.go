package labels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// GHClient implements Client using the GitHub CLI (`gh`). Repo is an
// "owner/name" slug.
type GHClient struct {
	Repo string
}

// errBlocked marks failures that should be reported as blocked, such as
// missing auth or insufficient permissions, rather than retried.
type errBlocked struct{ err error }

func (e errBlocked) Error() string { return e.err.Error() }
func (e errBlocked) Unwrap() error { return e.err }

// IsBlocked reports whether err represents an auth or permission failure.
func IsBlocked(err error) bool {
	var b errBlocked
	return errors.As(err, &b)
}

func (c GHClient) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		wrapped := fmt.Errorf("gh %s: %v: %s", strings.Join(args, " "), err, msg)
		if isAuthOrPermission(msg) {
			return nil, errBlocked{err: wrapped}
		}
		return nil, wrapped
	}
	return stdout.Bytes(), nil
}

func isAuthOrPermission(msg string) bool {
	lower := strings.ToLower(msg)
	for _, s := range []string{
		"authentication", "not logged", "gh auth login",
		"http 401", "http 403", "permission", "must have admin",
		"resource not accessible", "could not resolve to a repository",
	} {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// List returns the repo's current labels.
func (c GHClient) List(ctx context.Context) ([]Label, error) {
	out, err := c.run(ctx, "label", "list", "-R", c.Repo, "--limit", "200", "--json", "name,description,color")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Color       string `json:"color"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse gh label list: %w", err)
	}
	labels := make([]Label, 0, len(raw))
	for _, r := range raw {
		labels = append(labels, Label{Name: r.Name, Description: r.Description, Color: r.Color})
	}
	return labels, nil
}

// Create adds a new label.
func (c GHClient) Create(ctx context.Context, l Label) error {
	_, err := c.run(ctx, "label", "create", l.Name, "-R", c.Repo,
		"--description", l.Description, "--color", l.Color)
	return err
}

// Update edits an existing label's description and color, leaving its name.
func (c GHClient) Update(ctx context.Context, l Label) error {
	_, err := c.run(ctx, "label", "edit", l.Name, "-R", c.Repo,
		"--description", l.Description, "--color", l.Color)
	return err
}

var slugPattern = regexp.MustCompile(`^(?:https://github\.com/|git@github\.com:|ssh://git@github\.com/)([^/]+)/([^/]+?)(?:\.git)?/?$`)

// RepoSlug extracts an "owner/name" slug from a GitHub remote URL. It supports
// SSH (git@github.com:owner/name.git) and HTTPS (https://github.com/owner/name)
// forms, and rejects other hosts or extra path segments.
func RepoSlug(url string) (string, error) {
	url = strings.TrimSpace(url)
	m := slugPattern.FindStringSubmatch(url)
	if m == nil {
		return "", fmt.Errorf("cannot parse GitHub owner/name from %q", url)
	}
	return m[1] + "/" + m[2], nil
}

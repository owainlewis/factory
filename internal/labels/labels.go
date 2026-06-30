// Package labels keeps the standard Factory labels in sync on a GitHub repo.
//
// Factory only manages its own labels. It creates missing standard labels and
// normalizes the description and color of standard labels that already exist.
// It never deletes or renames a label it does not own.
package labels

import (
	"context"
	"fmt"
	"strings"
)

// Label is a GitHub issue label.
type Label struct {
	Name        string
	Description string
	Color       string
}

// Standard returns the Factory labels every managed repo should have. Colors
// are hex without the leading '#', matching the GitHub API.
func Standard() []Label {
	return []Label{
		{Name: "factory-ready", Description: "An agent may work this issue now", Color: "0e8a16"},
		{Name: "factory-triage", Description: "Needs clarification, acceptance criteria, or scope shaping", Color: "fbca04"},
		{Name: "factory-needs-human", Description: "Needs a human decision before implementation", Color: "b60205"},
		{Name: "factory-blocked", Description: "Blocked until a named dependency is resolved", Color: "d93f0b"},
	}
}

// Client talks to one GitHub repo's labels.
type Client interface {
	List(ctx context.Context) ([]Label, error)
	Create(ctx context.Context, label Label) error
	Update(ctx context.Context, label Label) error
}

// Plan compares the standard labels against the labels that already exist and
// returns the labels to create and the standard labels to update.
//
// A missing standard label is created. An existing standard label is updated
// only to fill an empty description or color; Factory never overwrites a
// non-empty description or color a human chose. Non-Factory labels are never
// returned and so are left untouched.
func Plan(existing []Label) (create, update []Label) {
	byName := make(map[string]Label, len(existing))
	for _, l := range existing {
		byName[l.Name] = l
	}

	for _, want := range Standard() {
		have, ok := byName[want.Name]
		if !ok {
			create = append(create, want)
			continue
		}

		filled := have
		changed := false
		if strings.TrimSpace(have.Description) == "" && want.Description != "" {
			filled.Description = want.Description
			changed = true
		}
		if strings.TrimSpace(have.Color) == "" && want.Color != "" {
			filled.Color = want.Color
			changed = true
		}
		if changed {
			update = append(update, filled)
		}
	}
	return create, update
}

// Report records what Sync did.
type Report struct {
	Created   []string
	Updated   []string
	Unchanged []string
}

// Sync ensures the standard Factory labels exist and match on the repo behind
// client. It lists current labels, applies the plan, and returns a report.
func Sync(ctx context.Context, client Client) (Report, error) {
	existing, err := client.List(ctx)
	if err != nil {
		return Report{}, err
	}

	create, update := Plan(existing)
	report := Report{}

	for _, l := range create {
		if err := client.Create(ctx, l); err != nil {
			return report, fmt.Errorf("create label %q: %w", l.Name, err)
		}
		report.Created = append(report.Created, l.Name)
	}
	for _, l := range update {
		if err := client.Update(ctx, l); err != nil {
			return report, fmt.Errorf("update label %q: %w", l.Name, err)
		}
		report.Updated = append(report.Updated, l.Name)
	}

	changed := make(map[string]bool, len(create)+len(update))
	for _, l := range create {
		changed[l.Name] = true
	}
	for _, l := range update {
		changed[l.Name] = true
	}
	for _, l := range Standard() {
		if !changed[l.Name] {
			report.Unchanged = append(report.Unchanged, l.Name)
		}
	}
	return report, nil
}

// Package scaffold writes the repo-owned .factory/ contract into a target
// directory. The generated files are defaults; the target repo owns them.
package scaffold

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

//go:embed all:templates/factory
var templates embed.FS

const templateRoot = "templates/factory"

// Result reports what Init did for a single file.
type Result struct {
	// Path is the file path relative to the target directory.
	Path string
	// Created is true when the file was written, false when it already
	// existed and was left in place.
	Created bool
}

// Init writes the .factory/ contract under dir. Existing files are left in
// place unless force is true. It returns one Result per template file, sorted
// by path. The next suggested command is left to the caller to print.
func Init(dir string, force bool) ([]Result, error) {
	var results []Result

	err := fs.WalkDir(templates, templateRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(templateRoot, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(dir, ".factory", filepath.FromSlash(rel))
		relDest := filepath.Join(".factory", filepath.FromSlash(rel))

		if !force {
			if _, statErr := os.Stat(dest); statErr == nil {
				results = append(results, Result{Path: relDest, Created: false})
				return nil
			} else if !os.IsNotExist(statErr) {
				return statErr
			}
		}

		data, err := templates.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return err
		}
		results = append(results, Result{Path: relDest, Created: true})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Path < results[j].Path
	})
	return results, nil
}

// Report writes a human-readable summary of an Init run and the next
// suggested command.
func Report(w io.Writer, dir string, results []Result) {
	created := 0
	for _, r := range results {
		status := "created"
		if !r.Created {
			status = "exists"
		} else {
			created++
		}
		fmt.Fprintf(w, "%s\t%s\n", status, r.Path)
	}

	if created == 0 {
		fmt.Fprintln(w, "\nNothing to do. All Factory files already exist.")
		fmt.Fprintln(w, "Re-run with --force to overwrite them with defaults.")
		return
	}

	fmt.Fprintf(w, "\nInitialized Factory contract under %s\n", filepath.Join(dir, ".factory"))
	fmt.Fprintln(w, "Edit the generated files. They are repo-owned.")
	fmt.Fprintln(w, "\nNext:")
	fmt.Fprintln(w, "  Review .factory/STANDARDS.md and .factory/AGENTS.md")
	fmt.Fprintln(w, "  Register this repo in your Factory config.yaml")
}

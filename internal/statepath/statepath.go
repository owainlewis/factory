package statepath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CanonicalProspective resolves symlinks in the nearest existing ancestor,
// preserving any path components that do not exist yet.
func CanonicalProspective(path string) (string, error) {
	var missing []string
	current := path
	for {
		canonical, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				canonical = filepath.Join(canonical, missing[index])
			}
			return canonical, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing ancestor for %s", path)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

// FindV1DatabaseMarker finds a Factory V1 database at or above path.
func FindV1DatabaseMarker(path string) (string, bool, error) {
	for {
		marker := filepath.Join(path, "factory.sqlite3")
		if info, err := os.Lstat(marker); err == nil {
			if info.Mode().IsRegular() {
				return marker, true, nil
			}
			return "", false, fmt.Errorf("inspect possible V1 database marker %s: not a regular file", marker)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", false, fmt.Errorf("inspect possible V1 database marker: %w", err)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", false, nil
		}
		path = parent
	}
}

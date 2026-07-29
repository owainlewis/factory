package worker

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	defaultServer        = "http://127.0.0.1:7337"
	defaultMaxConcurrent = 1
	maxConcurrent        = 4
)

type RepositoryConfig struct {
	Path string `toml:"path"`
}

type Config struct {
	Server        string                      `toml:"server"`
	Name          string                      `toml:"name"`
	MaxConcurrent int                         `toml:"max_concurrent"`
	DataDirectory string                      `toml:"data_directory"`
	Repositories  map[string]RepositoryConfig `toml:"repositories"`
}

type Repository struct {
	Key            string
	Path           string
	RemoteIdentity string
}

func LoadConfig(path string) (Config, error) {
	var config Config
	metadata, err := toml.DecodeFile(path, &config)
	if err != nil {
		return Config{}, fmt.Errorf("load worker configuration: %w", err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) != 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		sort.Strings(keys)
		return Config{}, fmt.Errorf("unknown worker configuration fields: %s", strings.Join(keys, ", "))
	}
	if config.Server == "" {
		config.Server = defaultServer
	}
	if config.MaxConcurrent == 0 {
		config.MaxConcurrent = defaultMaxConcurrent
	}
	if config.DataDirectory == "" {
		return Config{}, errors.New("data_directory is required")
	}
	if !filepath.IsAbs(config.DataDirectory) {
		config.DataDirectory = filepath.Join(filepath.Dir(path), config.DataDirectory)
	}
	for key, repository := range config.Repositories {
		if !filepath.IsAbs(repository.Path) {
			repository.Path = filepath.Join(filepath.Dir(path), repository.Path)
			config.Repositories[key] = repository
		}
	}
	return config, validateConfig(config)
}

func validateConfig(config Config) error {
	if err := validateServerURL(config.Server); err != nil {
		return err
	}
	if strings.TrimSpace(config.Name) == "" || len(config.Name) > 200 {
		return errors.New("name is required and must be at most 200 bytes")
	}
	if config.MaxConcurrent < 1 || config.MaxConcurrent > maxConcurrent {
		return fmt.Errorf("max_concurrent must be between 1 and %d", maxConcurrent)
	}
	if strings.TrimSpace(config.DataDirectory) == "" {
		return errors.New("data_directory is required")
	}
	if len(config.Repositories) == 0 {
		return errors.New("at least one repository is required")
	}
	for key, repository := range config.Repositories {
		if strings.TrimSpace(key) == "" || len(key) > 200 || key != strings.TrimSpace(key) {
			return errors.New("repository keys are required, must not have surrounding whitespace, and must be at most 200 bytes")
		}
		if strings.TrimSpace(repository.Path) == "" {
			return fmt.Errorf("repository %q path is required", key)
		}
	}
	return nil
}

func validateServerURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("parse server URL: %w", err)
	}
	if parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("server must be a plain loopback HTTP URL without credentials, query, or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return errors.New("server URL must not contain a path")
	}
	host := parsed.Hostname()
	if host == "" || parsed.Port() == "" {
		return errors.New("server URL must include a loopback host and port")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			return errors.New("server URL host must be loopback")
		}
		return nil
	}
	if !strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return errors.New("server URL host must be a loopback IP or localhost")
	}
	addresses, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve server URL host: %w", err)
	}
	if len(addresses) == 0 {
		return errors.New("server URL host resolved to no addresses")
	}
	for _, address := range addresses {
		if !address.IsLoopback() {
			return errors.New("server URL host must resolve only to loopback")
		}
	}
	return nil
}

func resolveDataDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve data directory: %w", err)
	}
	if marker, found, err := findV1DatabaseMarker(absolute); err != nil {
		return "", err
	} else if found {
		return "", fmt.Errorf("refusing a worker data directory below V1 state at %s", marker)
	}
	if info, err := os.Lstat(absolute); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("data_directory must be a real directory, not a symlink")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect data directory: %w", err)
	}
	canonical, err := canonicalProspectivePath(absolute)
	if err != nil {
		return "", err
	}
	if marker, found, err := findV1DatabaseMarker(canonical); err != nil {
		return "", err
	} else if found {
		return "", fmt.Errorf("refusing a worker data directory below V1 state at %s", marker)
	}
	if err := os.MkdirAll(canonical, 0o700); err != nil {
		return "", fmt.Errorf("create data directory: %w", err)
	}
	info, err := os.Lstat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect data directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("data_directory must be a real directory, not a symlink")
	}
	canonical, err = filepath.EvalSymlinks(canonical)
	if err != nil {
		return "", fmt.Errorf("canonicalize data directory: %w", err)
	}
	if err := os.Chmod(canonical, 0o700); err != nil {
		return "", fmt.Errorf("protect data directory: %w", err)
	}
	return canonical, nil
}

func canonicalProspectivePath(path string) (string, error) {
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
			return "", fmt.Errorf("canonicalize data directory: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("canonicalize data directory: no existing ancestor for %s", path)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func findV1DatabaseMarker(path string) (string, bool, error) {
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

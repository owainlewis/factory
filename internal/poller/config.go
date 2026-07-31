package poller

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/owainlewis/factory/internal/protocol"
)

const (
	defaultServer       = "http://127.0.0.1:7337"
	defaultPollEvery    = 30 * time.Second
	minimumPollEvery    = 10 * time.Second
	maximumPollEvery    = 24 * time.Hour
	defaultIssueTimeout = 2 * time.Hour
)

var (
	queueNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	sourcePattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,49}$`)
	githubProject    = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	issueKeyPattern  = regexp.MustCompile(`^(#[0-9]{1,99}|[A-Za-z0-9][A-Za-z0-9._#:/-]{0,99})$`)
)

type Config struct {
	Server        string        `toml:"server"`
	PollEvery     string        `toml:"poll_every"`
	DataDirectory string        `toml:"data_directory"`
	Queues        []QueueConfig `toml:"queues"`
	path          string
	interval      time.Duration
}

type QueueConfig struct {
	Name           string   `toml:"name"`
	Source         string   `toml:"source"`
	Command        []string `toml:"command"`
	Project        string   `toml:"project"`
	Status         string   `toml:"status"`
	Labels         []string `toml:"labels"`
	WorkerID       string   `toml:"worker_id"`
	RepositoryKey  string   `toml:"repository_key"`
	Prompt         string   `toml:"prompt"`
	TimeoutSeconds int      `toml:"timeout_seconds"`
}

func DefaultConfigPath() (string, error) {
	if explicit := os.Getenv("FACTORY_POLLER_CONFIG"); explicit != "" {
		return explicit, nil
	}
	root, err := factoryDataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "poller.toml"), nil
}

func LoadConfig(path string) (Config, error) {
	var config Config
	metadata, err := toml.DecodeFile(path, &config)
	if err != nil {
		return Config{}, fmt.Errorf("load poller configuration: %w", err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) != 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		sort.Strings(keys)
		return Config{}, fmt.Errorf("unknown poller configuration fields: %s", strings.Join(keys, ", "))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("resolve poller configuration path: %w", err)
	}
	config.path = absolute
	if config.Server == "" {
		config.Server = defaultServer
	}
	if config.PollEvery == "" {
		config.PollEvery = defaultPollEvery.String()
	}
	config.interval, err = time.ParseDuration(config.PollEvery)
	if err != nil {
		return Config{}, fmt.Errorf("poll_every must be a duration: %w", err)
	}
	if config.DataDirectory == "" {
		root, rootErr := factoryDataHome()
		if rootErr != nil {
			return Config{}, rootErr
		}
		config.DataDirectory = filepath.Join(root, "poller")
	} else if !filepath.IsAbs(config.DataDirectory) {
		config.DataDirectory = filepath.Join(filepath.Dir(absolute), config.DataDirectory)
	}
	return config, validateConfig(config)
}

func validateConfig(config Config) error {
	if err := validateServerURL(config.Server); err != nil {
		return err
	}
	if config.interval < minimumPollEvery || config.interval > maximumPollEvery {
		return errors.New("poll_every must be between 10s and 24h")
	}
	if strings.TrimSpace(config.DataDirectory) == "" {
		return errors.New("data_directory is required")
	}
	if len(config.Queues) == 0 {
		return errors.New("at least one queue is required")
	}
	names := make(map[string]bool, len(config.Queues))
	for index := range config.Queues {
		queue := &config.Queues[index]
		queue.Name = strings.TrimSpace(queue.Name)
		queue.Source = strings.ToLower(strings.TrimSpace(queue.Source))
		queue.Project = strings.TrimSpace(queue.Project)
		queue.Status = strings.TrimSpace(queue.Status)
		queue.WorkerID = strings.TrimSpace(queue.WorkerID)
		queue.RepositoryKey = strings.TrimSpace(queue.RepositoryKey)
		if !queueNamePattern.MatchString(queue.Name) {
			return fmt.Errorf("queue %d name must match %s", index+1, queueNamePattern)
		}
		if names[queue.Name] {
			return fmt.Errorf("queue name %q is duplicated", queue.Name)
		}
		names[queue.Name] = true
		if !sourcePattern.MatchString(queue.Source) {
			return fmt.Errorf("queue %q source must be a lowercase provider name", queue.Name)
		}
		if queue.Project == "" || len(queue.Project) > 500 {
			return fmt.Errorf("queue %q project is required and limited to 500 bytes", queue.Name)
		}
		if queue.Status == "" || len(queue.Status) > 200 {
			return fmt.Errorf("queue %q status is required and limited to 200 bytes", queue.Name)
		}
		if queue.Source == "github" {
			if len(queue.Command) != 0 {
				return fmt.Errorf("queue %q must not set command for the built-in github source", queue.Name)
			}
			if !githubProject.MatchString(queue.Project) {
				return fmt.Errorf("queue %q GitHub project must be owner/repository", queue.Name)
			}
			if queue.Status != "open" && queue.Status != "closed" {
				return fmt.Errorf("queue %q GitHub status must be open or closed", queue.Name)
			}
		} else if len(queue.Command) == 0 || strings.TrimSpace(queue.Command[0]) == "" {
			return fmt.Errorf("queue %q source %q requires a command", queue.Name, queue.Source)
		}
		if len(queue.Labels) > 20 {
			return fmt.Errorf("queue %q has more than 20 labels", queue.Name)
		}
		for labelIndex, label := range queue.Labels {
			if label == "" || label != strings.TrimSpace(label) || len(label) > 200 {
				return fmt.Errorf("queue %q labels must be non-empty, trimmed, and at most 200 bytes", queue.Name)
			}
			for previousIndex := 0; previousIndex < labelIndex; previousIndex++ {
				duplicate := queue.Labels[previousIndex] == label
				if queue.Source == "github" {
					duplicate = strings.EqualFold(queue.Labels[previousIndex], label)
				}
				if duplicate {
					return fmt.Errorf("queue %q label %q is duplicated", queue.Name, label)
				}
			}
		}
		if queue.Source != "github" && (queue.WorkerID == "" || queue.RepositoryKey == "") {
			return fmt.Errorf(
				"queue %q worker_id and repository_key are required for non-GitHub sources",
				queue.Name,
			)
		}
		if strings.TrimSpace(queue.Prompt) == "" {
			return fmt.Errorf("queue %q prompt is required", queue.Name)
		}
		if len(queue.Prompt) > protocol.MaxDescriptionBytes/2 {
			return fmt.Errorf("queue %q prompt is limited to 32 KiB", queue.Name)
		}
		if queue.TimeoutSeconds == 0 {
			queue.TimeoutSeconds = int(defaultIssueTimeout.Seconds())
		}
		if queue.TimeoutSeconds < 1 || queue.TimeoutSeconds > int(protocol.MaxTimeout/time.Second) {
			return fmt.Errorf("queue %q timeout_seconds must be between 1 and 28800", queue.Name)
		}
	}
	return nil
}

func validateServerURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("parse server URL: %w", err)
	}
	if parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("server must be a plain loopback HTTP URL without credentials, path, query, or fragment")
	}
	host := parsed.Hostname()
	if host == "" || parsed.Port() == "" {
		return errors.New("server must include a loopback host and port")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			return errors.New("server host must be loopback")
		}
		return nil
	}
	if !strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return errors.New("server host must be a loopback IP or localhost")
	}
	addresses, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve server host: %w", err)
	}
	for _, address := range addresses {
		if !address.IsLoopback() {
			return errors.New("server host must resolve only to loopback")
		}
	}
	return nil
}

func factoryDataHome() (string, error) {
	if value := os.Getenv("FACTORY_DATA_HOME"); value != "" {
		return value, nil
	}
	if value := os.Getenv("FACTORY_V2_DATA_HOME"); value != "" {
		return value, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".factory"), nil
}

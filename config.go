package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	defaultInterval        = 7 * 24 * time.Hour
	defaultSnooze          = 24 * time.Hour
	defaultApprovalTimeout = 60 * time.Second
)

type Config struct {
	Interval        string  `toml:"interval"`
	Snooze          string  `toml:"snooze"`
	ApprovalTimeout string  `toml:"approval_timeout"`
	Jobs            []Job   `toml:"jobs"`
	Groups          []Group `toml:"groups"`
	selectedGroup   string
}

type Group struct {
	Name     string   `toml:"name"`
	Interval string   `toml:"interval"`
	Jobs     []string `toml:"jobs"`
}

type Job struct {
	Name     string   `toml:"name"`
	Scope    string   `toml:"scope"`
	Interval string   `toml:"interval"`
	Command  string   `toml:"command"`
	Commands []string `toml:"commands"`
}

func configCommand(configPath, stateDir string, out, errOut io.Writer) int {
	absoluteConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	absoluteStateDir, err := filepath.Abs(stateDir)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	fmt.Fprintf(out, "Configuration file: %s\n", absoluteConfigPath)
	fmt.Fprintf(out, "State directory: %s\n", absoluteStateDir)
	return 0
}

func loadConfig(path string) (Config, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	meta, err := toml.DecodeFile(absPath, &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("load config %s: %w", absPath, err)
	}
	if keys := meta.Undecoded(); len(keys) > 0 {
		unknown := make([]string, len(keys))
		for i, key := range keys {
			unknown[i] = key.String()
		}
		return Config{}, fmt.Errorf("unknown config keys: %s", strings.Join(unknown, ", "))
	}

	interval, snooze, approvalTimeout, err := cfg.durations()
	if err != nil {
		return Config{}, err
	}
	if interval <= 0 {
		return Config{}, fmt.Errorf("interval must be positive")
	}
	if snooze < 0 {
		return Config{}, fmt.Errorf("snooze must not be negative")
	}
	if approvalTimeout <= 0 {
		return Config{}, fmt.Errorf("approval_timeout must be positive")
	}
	if len(cfg.Jobs) == 0 {
		return Config{}, fmt.Errorf("at least one job is required in config.toml")
	}
	seen := make(map[string]bool)
	if err := validateJobs(cfg.Jobs, "jobs", seen); err != nil {
		return Config{}, err
	}
	if err := validateGroups(cfg.Groups, cfg.Jobs); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateJobs(jobs []Job, path string, seen map[string]bool) error {
	for i := range jobs {
		job := &jobs[i]
		if strings.TrimSpace(job.Name) == "" {
			return fmt.Errorf("%s[%d].name is required", path, i)
		}
		if seen[job.Name] {
			return fmt.Errorf("duplicate job name %q", job.Name)
		}
		seen[job.Name] = true
		if job.Scope == "" {
			job.Scope = "user"
		}
		if job.Scope != "user" && job.Scope != "system" {
			return fmt.Errorf("%s[%d].scope must be user or system", path, i)
		}
		if job.Interval != "" {
			interval, err := time.ParseDuration(job.Interval)
			if err != nil {
				return fmt.Errorf("invalid %s[%s].interval %q: %w", path, job.Name, job.Interval, err)
			}
			if interval <= 0 {
				return fmt.Errorf("%s[%s].interval must be positive", path, job.Name)
			}
		}
		if job.Command != "" && len(job.Commands) > 0 {
			return fmt.Errorf("%s[%s].command and commands cannot both be set", path, job.Name)
		}
		if len(job.Commands) == 0 {
			if strings.TrimSpace(job.Command) == "" {
				return fmt.Errorf("%s[%s].command is required", path, job.Name)
			}
			if strings.IndexByte(job.Command, 0) >= 0 {
				return fmt.Errorf("%s[%s].command contains a NUL byte", path, job.Name)
			}
			continue
		}
		for i, command := range job.Commands {
			if strings.TrimSpace(command) == "" {
				return fmt.Errorf("%s[%s].commands[%d] is required", path, job.Name, i)
			}
			if strings.IndexByte(command, 0) >= 0 {
				return fmt.Errorf("%s[%s].commands[%d] contains a NUL byte", path, job.Name, i)
			}
		}
	}
	return nil
}

func validateGroups(groups []Group, jobs []Job) error {
	jobNames := make(map[string]bool, len(jobs))
	for _, job := range jobs {
		jobNames[job.Name] = true
	}
	groupNames := make(map[string]bool, len(groups))
	jobGroups := make(map[string]string)
	for i, group := range groups {
		path := fmt.Sprintf("groups[%d]", i)
		if strings.TrimSpace(group.Name) == "" {
			return fmt.Errorf("%s.name is required", path)
		}
		if groupNames[group.Name] {
			return fmt.Errorf("duplicate group name %q", group.Name)
		}
		groupNames[group.Name] = true
		if group.Interval != "" {
			parsed, err := time.ParseDuration(group.Interval)
			if err != nil {
				return fmt.Errorf("invalid %s.interval %q: %w", path, group.Interval, err)
			}
			if parsed <= 0 {
				return fmt.Errorf("%s.interval must be positive", path)
			}
		}
		for _, jobName := range group.Jobs {
			if strings.TrimSpace(jobName) == "" {
				return fmt.Errorf("%s.jobs contains an empty job name", path)
			}
			if !jobNames[jobName] {
				return fmt.Errorf("unknown job %q in %s.jobs", jobName, path)
			}
			if previous, ok := jobGroups[jobName]; ok {
				return fmt.Errorf("job %q belongs to multiple groups: %s and %s", jobName, previous, group.Name)
			}
			jobGroups[jobName] = group.Name
		}
	}
	return nil
}

func (j Job) commands() []string {
	if len(j.Commands) > 0 {
		return j.Commands
	}
	if j.Command != "" {
		return []string{j.Command}
	}
	return nil
}

func (c Config) selectGroup(name string) (Config, error) {
	for _, group := range c.Groups {
		if group.Name == name {
			c.selectedGroup = name
			return c, nil
		}
	}
	return Config{}, fmt.Errorf("unknown group %q", name)
}

func (c Config) allJobs() []Job {
	groupIntervals := make(map[string]string)
	for _, group := range c.Groups {
		for _, jobName := range group.Jobs {
			groupIntervals[jobName] = group.Interval
		}
	}
	jobs := make([]Job, 0, len(c.Jobs))
	for _, job := range c.Jobs {
		if job.Interval == "" {
			job.Interval = groupIntervals[job.Name]
		}
		jobs = append(jobs, job)
	}
	return jobs
}

func (c Config) orderedJobs() []Job {
	jobs := c.allJobs()
	if c.selectedGroup == "" {
		return jobs
	}
	selected := make(map[string]bool)
	for _, group := range c.Groups {
		if group.Name == c.selectedGroup {
			for _, jobName := range group.Jobs {
				selected[jobName] = true
			}
			break
		}
	}
	filtered := make([]Job, 0, len(selected))
	for _, job := range jobs {
		if selected[job.Name] {
			filtered = append(filtered, job)
		}
	}
	return filtered
}

func effectiveJobInterval(job Job, globalInterval time.Duration, override bool) (time.Duration, error) {
	if override || job.Interval == "" {
		return globalInterval, nil
	}
	interval, err := time.ParseDuration(job.Interval)
	if err != nil {
		return 0, fmt.Errorf("invalid jobs[%s].interval %q: %w", job.Name, job.Interval, err)
	}
	if interval <= 0 {
		return 0, fmt.Errorf("jobs[%s].interval must be positive", job.Name)
	}
	return interval, nil
}

func (c Config) durations() (time.Duration, time.Duration, time.Duration, error) {
	interval := defaultInterval
	if c.Interval != "" {
		parsed, err := time.ParseDuration(c.Interval)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid interval %q: %w", c.Interval, err)
		}
		interval = parsed
	}
	snooze := defaultSnooze
	if c.Snooze != "" {
		parsed, err := time.ParseDuration(c.Snooze)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid snooze %q: %w", c.Snooze, err)
		}
		snooze = parsed
	}
	approvalTimeout := defaultApprovalTimeout
	if c.ApprovalTimeout != "" {
		parsed, err := time.ParseDuration(c.ApprovalTimeout)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid approval_timeout %q: %w", c.ApprovalTimeout, err)
		}
		approvalTimeout = parsed
	}
	return interval, snooze, approvalTimeout, nil
}

func defaultConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "config.toml"
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "upkeep", "config.toml")
}

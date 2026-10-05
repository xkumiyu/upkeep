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
	defaultSnooze          = 24 * time.Hour
	defaultApprovalTimeout = 60 * time.Second
)

type Config struct {
	Snooze          string     `toml:"snooze"`
	ApprovalTimeout string     `toml:"approval_timeout"`
	Workflows       []Workflow `toml:"workflows"`
}

type Workflow struct {
	Name     string `toml:"name"`
	Interval string `toml:"interval"`
	Jobs     []Job  `toml:"jobs"`
}

type Job struct {
	Name     string   `toml:"name"`
	Scope    string   `toml:"scope"`
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

	snooze, approvalTimeout, err := cfg.durations()
	if err != nil {
		return Config{}, err
	}
	if snooze < 0 {
		return Config{}, fmt.Errorf("snooze must not be negative")
	}
	if approvalTimeout <= 0 {
		return Config{}, fmt.Errorf("approval_timeout must be positive")
	}
	if len(cfg.Workflows) == 0 {
		return Config{}, fmt.Errorf("at least one workflow is required in config.toml")
	}
	seen := make(map[string]bool)
	for i, workflow := range cfg.Workflows {
		path := fmt.Sprintf("workflows[%d]", i)
		if strings.TrimSpace(workflow.Name) == "" {
			return Config{}, fmt.Errorf("%s.name is required", path)
		}
		if seen[workflow.Name] {
			return Config{}, fmt.Errorf("duplicate workflow name %q", workflow.Name)
		}
		seen[workflow.Name] = true
		if _, err := workflow.duration(); err != nil {
			return Config{}, err
		}
		if len(workflow.Jobs) == 0 {
			return Config{}, fmt.Errorf("%s.jobs requires at least one job", path)
		}
		if err := validateJobs(cfg.Workflows[i].Jobs, path+".jobs", make(map[string]bool)); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

func (w Workflow) duration() (time.Duration, error) {
	if w.Interval == "" {
		return 0, fmt.Errorf("workflows[%s].interval is required", w.Name)
	}
	interval, err := time.ParseDuration(w.Interval)
	if err != nil {
		return 0, fmt.Errorf("invalid workflows[%s].interval %q: %w", w.Name, w.Interval, err)
	}
	if interval <= 0 {
		return 0, fmt.Errorf("workflows[%s].interval must be positive", w.Name)
	}
	return interval, nil
}

func validateJobs(jobs []Job, path string, seen map[string]bool) error {
	for i := range jobs {
		job := &jobs[i]
		if strings.TrimSpace(job.Name) == "" {
			return fmt.Errorf("%s[%d].name is required", path, i)
		}
		if seen[job.Name] {
			return fmt.Errorf("%s: duplicate job name %q", path, job.Name)
		}
		seen[job.Name] = true
		if job.Scope == "" {
			job.Scope = "user"
		}
		if job.Scope != "user" && job.Scope != "system" {
			return fmt.Errorf("%s[%d].scope must be user or system", path, i)
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

func (j Job) commands() []string {
	if len(j.Commands) > 0 {
		return j.Commands
	}
	if j.Command != "" {
		return []string{j.Command}
	}
	return nil
}

func (c Config) selectWorkflows(names []string) (Config, error) {
	if len(names) == 0 {
		return c, nil
	}
	selected := make(map[string]bool)
	for _, name := range names {
		selected[name] = true
	}
	workflows := make([]Workflow, 0, len(c.Workflows))
	for _, workflow := range c.Workflows {
		if selected[workflow.Name] {
			workflows = append(workflows, workflow)
			delete(selected, workflow.Name)
		}
	}
	for _, name := range names {
		if selected[name] {
			return Config{}, fmt.Errorf("unknown workflow %q", name)
		}
	}
	c.Workflows = workflows
	return c, nil
}

func (c Config) durations() (time.Duration, time.Duration, error) {
	snooze := defaultSnooze
	if c.Snooze != "" {
		parsed, err := time.ParseDuration(c.Snooze)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid snooze %q: %w", c.Snooze, err)
		}
		snooze = parsed
	}
	approvalTimeout := defaultApprovalTimeout
	if c.ApprovalTimeout != "" {
		parsed, err := time.ParseDuration(c.ApprovalTimeout)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid approval_timeout %q: %w", c.ApprovalTimeout, err)
		}
		approvalTimeout = parsed
	}
	return snooze, approvalTimeout, nil
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

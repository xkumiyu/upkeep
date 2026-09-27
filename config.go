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
	Interval        string `toml:"interval"`
	Snooze          string `toml:"snooze"`
	ApprovalTimeout string `toml:"approval_timeout"`
	Jobs            []Job  `toml:"jobs"`
}

type Job struct {
	Name     string `toml:"name"`
	Scope    string `toml:"scope"`
	Interval string `toml:"interval"`
	Command  string `toml:"command"`
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
		return Config{}, fmt.Errorf("at least one job is required")
	}

	seen := make(map[string]bool)
	for i, job := range cfg.Jobs {
		if strings.TrimSpace(job.Name) == "" {
			return Config{}, fmt.Errorf("jobs[%d].name is required", i)
		}
		if seen[job.Name] {
			return Config{}, fmt.Errorf("duplicate job name %q", job.Name)
		}
		seen[job.Name] = true
		if job.Scope == "" {
			cfg.Jobs[i].Scope = "user"
			job.Scope = "user"
		}
		if job.Scope != "user" && job.Scope != "system" {
			return Config{}, fmt.Errorf("jobs[%d].scope must be user or system", i)
		}
		if job.Interval != "" {
			interval, err := time.ParseDuration(job.Interval)
			if err != nil {
				return Config{}, fmt.Errorf("invalid jobs[%s].interval %q: %w", job.Name, job.Interval, err)
			}
			if interval <= 0 {
				return Config{}, fmt.Errorf("jobs[%s].interval must be positive", job.Name)
			}
		}
		if strings.TrimSpace(job.Command) == "" {
			return Config{}, fmt.Errorf("jobs[%s].command is required", job.Name)
		}
		if strings.IndexByte(job.Command, 0) >= 0 {
			return Config{}, fmt.Errorf("jobs[%s].command contains a NUL byte", job.Name)
		}
	}
	return cfg, nil
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

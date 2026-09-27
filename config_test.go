package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigRejectsEmptyCommand(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "upkeep.toml")
	if err := os.WriteFile(configPath, []byte(`
interval = "24h"

[[jobs]]
name = "empty"
scope = "user"
command = "   "
`), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := loadConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "command is required") {
		t.Fatalf("loadConfig error = %v, want a required command error", err)
	}
}

func TestDefaultConfigPathUsesConfigToml(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	configPath := filepath.Join(configHome, "upkeep", "config.toml")
	if got := defaultConfigPath(); got != configPath {
		t.Fatalf("default config path = %q, want %q", got, configPath)
	}
}

func TestLoadConfigRejectsInvalidDuration(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "upkeep.toml")
	if err := os.WriteFile(configPath, []byte(`
interval = "not-a-duration"

[[jobs]]
name = "user"
scope = "user"
command = "true"
`), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := loadConfig(configPath)
	if err == nil {
		t.Fatal("expected an invalid duration error")
	}
}

func TestLoadConfigDefaultsJobScopeAndSupportsJobInterval(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
[[jobs]]
name = "user"
interval = "24h"
command = "true"
`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Jobs[0].Scope != "user" {
		t.Fatalf("default job scope = %q, want user", cfg.Jobs[0].Scope)
	}
	if cfg.Jobs[0].Interval != "24h" {
		t.Fatalf("job interval = %q, want 24h", cfg.Jobs[0].Interval)
	}
}

func TestLoadConfigSupportsApprovalTimeout(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
snooze = "2h"
approval_timeout = "45s"

[[jobs]]
name = "user"
command = "true"
`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	_, snooze, approvalTimeout, err := cfg.durations()
	if err != nil {
		t.Fatal(err)
	}
	if snooze != 2*time.Hour {
		t.Fatalf("snooze = %s, want 2h", snooze)
	}
	if approvalTimeout != 45*time.Second {
		t.Fatalf("approval timeout = %s, want 45s", approvalTimeout)
	}
}

func TestConfigUsesDefaultApprovalTimeout(t *testing.T) {
	_, _, approvalTimeout, err := (Config{}).durations()
	if err != nil {
		t.Fatal(err)
	}
	if approvalTimeout != 60*time.Second {
		t.Fatalf("approval timeout = %s, want 60s", approvalTimeout)
	}
}

func TestLoadConfigRejectsNonPositiveJobInterval(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
[[jobs]]
name = "user"
interval = "0s"
command = "true"
`), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := loadConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "jobs[user].interval must be positive") {
		t.Fatalf("loadConfig error = %v, want a positive job interval error", err)
	}
}

func TestConfigCommandShowsPaths(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	stateDir := filepath.Join(t.TempDir(), "state")
	var output, errorsOutput strings.Builder
	code := runCLI([]string{
		"config",
		"--config", configPath,
		"--state-dir", stateDir,
	}, strings.NewReader(""), &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q", code, errorsOutput.String())
	}
	for _, want := range []string{"Configuration file: " + configPath, "State directory: " + stateDir} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("config output = %q, want %q", output.String(), want)
		}
	}
}

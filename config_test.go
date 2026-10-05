package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigRejectsEmptyCommand(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "upkeep.toml")
	if err := os.WriteFile(configPath, []byte(`

[[workflows]]
name = "dev-tools"
interval = "24h"
[[workflows.jobs]]
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

func TestLoadConfigRejectsMissingJobsWithConfigName(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := loadConfig(configPath)
	if err == nil || err.Error() != "at least one workflow is required in config.toml" {
		t.Fatalf("loadConfig error = %v, want missing jobs error naming config.toml", err)
	}
}

func TestLoadConfigSupportsCommands(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "upkeep.toml")
	if err := os.WriteFile(configPath, []byte(`
[[workflows]]
name = "dev-tools"
interval = "24h"
[[workflows.jobs]]
name = "packages"
commands = [
  "printf first",
  "printf second",
]
`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Workflows[0].Jobs[0].Commands, []string{"printf first", "printf second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
}

func TestLoadConfigRejectsCommandAndCommands(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "upkeep.toml")
	if err := os.WriteFile(configPath, []byte(`
[[workflows]]
name = "dev-tools"
interval = "24h"
[[workflows.jobs]]
name = "packages"
command = "printf one"
commands = ["printf two"]
`), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := loadConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "command and commands cannot both be set") {
		t.Fatalf("loadConfig error = %v, want command conflict error", err)
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
[[workflows]]
name = "dev-tools"
interval = "not-a-duration"
[[workflows.jobs]]
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

func TestLoadConfigSupportsApprovalTimeout(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
snooze = "2h"
approval_timeout = "45s"

[[workflows]]
name = "dev-tools"
interval = "24h"
[[workflows.jobs]]
name = "user"
command = "true"
`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	snooze, approvalTimeout, err := cfg.durations()
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
	_, approvalTimeout, err := (Config{}).durations()
	if err != nil {
		t.Fatal(err)
	}
	if approvalTimeout != 60*time.Second {
		t.Fatalf("approval timeout = %s, want 60s", approvalTimeout)
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

func TestWorkflowConfigValidation(t *testing.T) {
	valid := `[[workflows]]
name="dev-tools"
interval="24h"
[[workflows.jobs]]
name="mise"
command="true"
`
	for _, test := range []struct{ name, config, want string }{
		{"required interval", strings.Replace(valid, `interval="24h"`, "", 1), "interval is required"},
		{"zero interval", strings.Replace(valid, `interval="24h"`, `interval="0s"`, 1), "must be positive"},
		{"negative interval", strings.Replace(valid, `interval="24h"`, `interval="-1h"`, 1), "must be positive"},
		{"invalid interval", strings.Replace(valid, `interval="24h"`, `interval="invalid"`, 1), "invalid workflows"},
		{"global interval", `interval="24h"` + "\n" + valid, "unknown config keys: interval"},
		{"flat jobs", `[[jobs]]` + "\nname=\"old\"\ncommand=\"true\"", "unknown config keys: jobs"},
		{"groups", valid + "\n[[groups]]\nname=\"old\"", "unknown config keys: groups"},
		{"retry interval", `retry_interval="1h"` + "\n" + valid, "unknown config keys: retry_interval"},
		{"job interval", valid + `interval="1h"`, "workflows.jobs.interval"},
		{"workflow retry", strings.Replace(valid, "[[workflows.jobs]]", "retry_interval=\"1h\"\n[[workflows.jobs]]", 1), "workflows.retry_interval"},
		{"job retry", valid + `retry_interval="1h"`, "workflows.jobs.retry_interval"},
		{"duplicate workflow", valid + valid, "duplicate workflow name"},
		{"duplicate job", valid + "[[workflows.jobs]]\nname=\"mise\"\ncommand=\"true\"", "duplicate job name"},
		{"empty workflow", "[[workflows]]\nname=\"empty\"\ninterval=\"1h\"", "requires at least one job"},
		{"scope", valid + `scope="root"`, "scope must be user or system"},
		{"nul command", strings.Replace(valid, `command="true"`, `command="\u0000"`, 1), "NUL byte"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(test.config), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want %s", err, test.want)
			}
		})
	}
}

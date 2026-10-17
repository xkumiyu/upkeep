package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAcquireLockIsExclusive(t *testing.T) {
	stateDir := t.TempDir()
	release, err := acquireLock(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, err = acquireLock(stateDir)
	if !errors.Is(err, errAlreadyRunning) {
		t.Fatalf("second lock error = %v, want errAlreadyRunning", err)
	}
}

func TestAcquireLockRemovesStaleOwner(t *testing.T) {
	stateDir := t.TempDir()
	lockDir := filepath.Join(stateDir, "lock")
	if err := os.Mkdir(lockDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, "owner"), []byte("pid=2147483647\n"), 0600); err != nil {
		t.Fatal(err)
	}

	release, err := acquireLock(stateDir)
	if err != nil {
		t.Fatalf("acquire stale lock: %v", err)
	}
	defer release()

	data, err := os.ReadFile(filepath.Join(lockDir, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	if got := ownerPID(string(data)); got != os.Getpid() {
		t.Fatalf("owner pid = %d, want %d", got, os.Getpid())
	}
}

func statusTestConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[[workflows]]\nname=\"dev-tools\"\ninterval=\"24h\"\n[[workflows.jobs]]\nname=\"job\"\ncommand=\"true\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStatusCommandUsesHumanReadableLabels(t *testing.T) {
	stateDir := t.TempDir()
	history := WorkflowState{
		LastAttempt:  time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC),
		LastFinished: time.Date(2026, 9, 27, 0, 1, 0, 0, time.UTC),
		LastSuccess:  time.Date(2026, 9, 27, 0, 1, 0, 0, time.UTC),
		LastExitCode: 0,
		LastResultByJob: map[string]string{
			"system-packages": jobResultFailed,
			"unselected-job":  jobResultNotRun,
			"user-packages":   jobResultSucceeded,
		},
	}
	if err := saveState(stateDir, State{Workflows: map[string]*WorkflowState{"dev-tools": &history}}); err != nil {
		t.Fatal(err)
	}

	var output, errorsOutput strings.Builder
	code := statusCommand(statusTestConfig(t), stateDir, &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q", code, errorsOutput.String())
	}
	for _, want := range []string{
		`Workflow "dev-tools":`,
		"  Started:",
		"  Finished:",
		"  Result:   succeeded",
		"  Jobs (last run):",
		"    system-packages: failed",
		"    unselected-job: not run",
		"    user-packages: succeeded",
		"Last successful run:",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("status = %q, want %q", output.String(), want)
		}
	}
	if strings.Contains(output.String(), "last_") || strings.Contains(output.String(), "=") || strings.Contains(output.String(), "exit code") {
		t.Fatalf("status = %q, want human-readable labels", output.String())
	}
}

func TestStatusCommandReportsIncompleteRun(t *testing.T) {
	stateDir := t.TempDir()
	history := WorkflowState{
		LastAttempt:  time.Date(2026, 9, 27, 0, 2, 0, 0, time.UTC),
		LastFinished: time.Date(2026, 9, 27, 0, 1, 0, 0, time.UTC),
		LastSuccess:  time.Date(2026, 9, 27, 0, 1, 0, 0, time.UTC),
		LastExitCode: 0,
	}
	if err := saveState(stateDir, State{Workflows: map[string]*WorkflowState{"dev-tools": &history}}); err != nil {
		t.Fatal(err)
	}

	var output, errorsOutput strings.Builder
	code := statusCommand(statusTestConfig(t), stateDir, &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q", code, errorsOutput.String())
	}
	if !strings.Contains(output.String(), "Result:   incomplete") {
		t.Fatalf("status = %q, want incomplete result", output.String())
	}
	if strings.Contains(output.String(), "Result: succeeded") {
		t.Fatalf("status = %q, want no succeeded result", output.String())
	}
}

func TestStatusCommandReportsIncompleteRunWithoutFinish(t *testing.T) {
	stateDir := t.TempDir()
	history := WorkflowState{
		LastAttempt:  time.Date(2026, 9, 27, 0, 2, 0, 0, time.UTC),
		LastExitCode: incompleteExitCode,
	}
	if err := saveState(stateDir, State{Workflows: map[string]*WorkflowState{"dev-tools": &history}}); err != nil {
		t.Fatal(err)
	}

	var output, errorsOutput strings.Builder
	code := statusCommand(statusTestConfig(t), stateDir, &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q", code, errorsOutput.String())
	}
	if !strings.Contains(output.String(), "Result:   incomplete") {
		t.Fatalf("status = %q, want incomplete result", output.String())
	}
}

func TestStatusUsesCurrentConfigAndPreservesHistory(t *testing.T) {
	for _, savedHistory := range []bool{false, true} {
		for _, customConfig := range []bool{false, true} {
			t.Run(fmt.Sprintf("history=%t/customConfig=%t", savedHistory, customConfig), func(t *testing.T) {
				root := t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", root)
				path := filepath.Join(root, "upkeep", "config.toml")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if customConfig {
					path = filepath.Join(root, "custom.toml")
				}
				var config strings.Builder
				for _, name := range []string{"z-current", "renamed", "a-new"} {
					fmt.Fprintf(&config, "[[workflows]]\nname=%q\ninterval=\"24h\"\n[[workflows.jobs]]\nname=\"current-job\"\ncommand=\"true\"\n", name)
				}
				if err := os.WriteFile(path, []byte(config.String()), 0600); err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(root, "state")
				statePath := filepath.Join(dir, "workflows.toml")
				var before []byte
				if savedHistory {
					state := State{Workflows: map[string]*WorkflowState{
						"z-current": {LastAttempt: time.Now().Add(-time.Minute), LastFinished: time.Now(), LastExitCode: 1, LastResultByJob: map[string]string{"previous-job": jobResultFailed}},
						"removed":   {LastFinished: time.Now()},
						"old-name":  {LastFinished: time.Now()},
					}}
					if err := saveState(dir, state); err != nil {
						t.Fatal(err)
					}
					var err error
					before, err = os.ReadFile(statePath)
					if err != nil {
						t.Fatal(err)
					}
				}
				args := []string{"status", "--state-dir", dir}
				if customConfig {
					args = append(args, "--config", path)
				}
				var out, errs strings.Builder
				if code := runCLI(args, nil, &out, &errs); code != 0 {
					t.Fatalf("code=%d errors=%s", code, errs.String())
				}
				output := out.String()
				if strings.Count(output, "Workflow ") != 3 || strings.Contains(output, "removed") || strings.Contains(output, "old-name") {
					t.Fatal(output)
				}
				previous := -1
				for _, name := range []string{"z-current", "renamed", "a-new"} {
					index := strings.Index(output, fmt.Sprintf("Workflow %q:", name))
					if index <= previous {
						t.Fatalf("workflow order: %s", output)
					}
					previous = index
				}
				wantNew := 3
				if savedHistory {
					wantNew = 2
					if !strings.Contains(output, "Result:   failed") || !strings.Contains(output, "previous-job: failed") || strings.Contains(output, "current-job") {
						t.Fatalf("previous results lost: %s", output)
					}
				}
				if strings.Count(output, "Result:   no run yet") != wantNew || strings.Count(output, "Started:  never") != wantNew {
					t.Fatalf("new workflows missing: %s", output)
				}
				after, err := os.ReadFile(statePath)
				if savedHistory && (err != nil || string(after) != string(before)) {
					t.Fatalf("history changed: %s %v", after, err)
				}
				if !savedHistory {
					if _, err := os.Stat(dir); !os.IsNotExist(err) {
						t.Fatalf("status created state directory: %v", err)
					}
				}
			})
		}
	}
}

func TestStatusRejectsUnreadableOrInvalidConfig(t *testing.T) {
	for _, content := range []string{"missing", "[[invalid", "[[workflows]]\nname=\"incomplete\"\n"} {
		t.Run(content, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "config.toml")
			if content != "missing" {
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			dir := filepath.Join(root, "state")
			var out, errs strings.Builder
			if code := runCLI([]string{"status", "--config", path, "--state-dir", dir}, nil, &out, &errs); code != 1 || out.Len() != 0 || !strings.Contains(errs.String(), "upkeep:") {
				t.Fatalf("code=%d output=%s errors=%s", code, out.String(), errs.String())
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("status created state directory: %v", err)
			}
		})
	}
}

func TestWorkflowLegacyStatePreserved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")
	legacy := []byte("last_success = 2026-10-04T00:00:00Z\n")
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(dir)
	if err != nil || len(state.Workflows) != 0 {
		t.Fatalf("legacy used: %v %v", state, err)
	}
	if _, err := runOnceLocked(workflowConfig(Job{Name: "job", Command: "true"}), dir, "test", false, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(legacy) {
		t.Fatalf("legacy changed: %s %v", data, err)
	}
}

func TestWorkflowInterruptedProcessPreservesCheckpoints(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestWorkflowInterruptedProcessHelper$")
	cmd.Env = append(os.Environ(), "UPKEEP_INTERRUPTION_STATE="+dir)
	if err := cmd.Run(); err == nil {
		t.Fatal("helper must be killed")
	}
	state, err := loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, second := state.Workflows["first"], state.Workflows["second"]
	if first == nil || second == nil || first.LastFinished.IsZero() || !runIncomplete(*second) {
		t.Fatalf("workflow checkpoints missing: %+v", state)
	}
	if first.LastResultByJob["same"] != jobResultSucceeded || second.LastResultByJob["same"] != jobResultSucceeded || second.LastResultByJob["interrupt"] != jobResultIncomplete {
		t.Fatalf("job checkpoints missing: first=%+v second=%+v", first, second)
	}
	cfg := Config{Workflows: []Workflow{{Name: "first", Interval: "24h"}, {Name: "second", Interval: "24h"}}}
	due, err := dueWorkflows(cfg, state, false, time.Now())
	if err != nil || len(due) != 1 || due[0].Name != "second" {
		t.Fatalf("due=%v error=%v", due, err)
	}
}

func TestWorkflowInterruptedProcessHelper(t *testing.T) {
	dir := os.Getenv("UPKEEP_INTERRUPTION_STATE")
	if dir == "" {
		return
	}
	t.Setenv("UPKEEP_TEST_PARENT_PID", strconv.Itoa(os.Getpid()))
	cfg := Config{Workflows: []Workflow{
		{Name: "first", Interval: "24h", Jobs: []Job{{Name: "same", Command: "true"}}},
		{Name: "second", Interval: "24h", Jobs: []Job{{Name: "same", Command: "true"}, {Name: "interrupt", Command: `kill -KILL "$UPKEEP_TEST_PARENT_PID"`}}},
	}}
	if _, err := runOnceLocked(cfg, dir, "test", false, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	t.Fatal("helper unexpectedly completed")
}

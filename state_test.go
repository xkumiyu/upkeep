package main

import (
	"errors"
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
	code := statusCommand(stateDir, &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q", code, errorsOutput.String())
	}
	for _, want := range []string{
		`Workflow "dev-tools":`,
		"  Started:",
		"  Finished:",
		"  Result:   succeeded",
		"  Jobs:",
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
	code := statusCommand(stateDir, &output, &errorsOutput)
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
	code := statusCommand(stateDir, &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q", code, errorsOutput.String())
	}
	if !strings.Contains(output.String(), "Result:   incomplete") {
		t.Fatalf("status = %q, want incomplete result", output.String())
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

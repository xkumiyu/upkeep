package main

import (
	"errors"
	"os"
	"path/filepath"
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
	state := State{
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
	if err := saveState(stateDir, state); err != nil {
		t.Fatal(err)
	}

	var output, errorsOutput strings.Builder
	code := statusCommand(stateDir, &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q", code, errorsOutput.String())
	}
	for _, want := range []string{
		"Last run:",
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
	state := State{
		LastAttempt:  time.Date(2026, 9, 27, 0, 2, 0, 0, time.UTC),
		LastFinished: time.Date(2026, 9, 27, 0, 1, 0, 0, time.UTC),
		LastSuccess:  time.Date(2026, 9, 27, 0, 1, 0, 0, time.UTC),
		LastExitCode: 0,
	}
	if err := saveState(stateDir, state); err != nil {
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
	state := State{
		LastAttempt:  time.Date(2026, 9, 27, 0, 2, 0, 0, time.UTC),
		LastExitCode: incompleteExitCode,
	}
	if err := saveState(stateDir, state); err != nil {
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

func TestIsDue(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	state := State{LastSuccess: now.Add(-25 * time.Hour)}
	if !isDue(state, 24*time.Hour, now) {
		t.Fatal("expected an old successful run to be due")
	}

	state.LastSuccess = now.Add(-time.Hour)
	if isDue(state, 24*time.Hour, now) {
		t.Fatal("expected a recent successful run not to be due")
	}
}

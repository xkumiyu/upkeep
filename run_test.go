package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestExecuteJobForwardsStandardInput(t *testing.T) {
	job := Job{
		Name:    "interactive",
		Scope:   "user",
		Command: `read value && printf 'received %s' "$value"`,
	}
	var output strings.Builder
	if err := executeJob(job, strings.NewReader("direct input\n"), &output, false); err != nil {
		t.Fatalf("executeJob error = %v", err)
	}
	if output.String() != "received direct input" {
		t.Fatalf("output = %q, want forwarded input", output.String())
	}
}

func TestExecuteJobRunsCommandsInOrderAndStopsOnFailure(t *testing.T) {
	job := Job{
		Name:     "commands",
		Scope:    "user",
		Commands: []string{"printf first", "false", "printf third"},
	}
	var output strings.Builder
	if err := executeJob(job, strings.NewReader(""), &output, false); err == nil {
		t.Fatal("executeJob error = nil, want failure")
	}
	if got, want := output.String(), "first"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunUserJobWritesStateAndLog(t *testing.T) {
	configDir := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	configPath := filepath.Join(configDir, "upkeep.toml")
	if err := os.WriteFile(configPath, []byte(`
interval = "24h"

[[jobs]]
name = "user-packages"
scope = "user"
command = "printf updated && printf ' shell'"
`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	release, err := acquireLock(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	var output strings.Builder
	result, err := runOnceLocked(cfg, stateDir, "manual", false, strings.NewReader(""), &output, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 0 {
		t.Fatalf("failed jobs = %d", result.Failed)
	}
	if !strings.Contains(output.String(), "updated") {
		t.Fatalf("output = %q, want command output", output.String())
	}
	if strings.Contains(output.String(), "=") || !strings.Contains(output.String(), `Running user job "user-packages".`) {
		t.Fatalf("output = %q, want human-readable job output", output.String())
	}
	if strings.Count(output.String(), "----------------------------------------") < 2 {
		t.Fatalf("output = %q, want job separators", output.String())
	}

	state, err := loadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastSuccess.IsZero() || state.LastFinished.IsZero() {
		t.Fatalf("state timestamps were not written: %+v", state)
	}
	if state.LastResultByJob["user-packages"] != jobResultSucceeded {
		t.Fatalf("user job result = %q, want succeeded", state.LastResultByJob["user-packages"])
	}

	entries, err := os.ReadDir(filepath.Join(stateDir, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("log files = %d, want 1", len(entries))
	}
	logData, err := os.ReadFile(filepath.Join(stateDir, "logs", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "updated") {
		t.Fatalf("log = %q, want command output", logData)
	}
	if strings.Contains(string(logData), "=") {
		t.Fatalf("log = %q, want human-readable job output", logData)
	}
}

func TestRunOnceLockedCheckpointsEachCompletedJob(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Setenv("UPKEEP_TEST_STATE_FILE", filepath.Join(stateDir, "state.toml"))
	cfg := Config{Jobs: []Job{
		{Name: "first-job", Scope: "user", Command: ":"},
		{Name: "second-job", Scope: "user", Command: `grep -F 'first-job = "succeeded"' "$UPKEEP_TEST_STATE_FILE" >/dev/null`},
	}}
	release, err := acquireLock(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	var output strings.Builder
	result, err := runOnceLocked(cfg, stateDir, "manual", false, strings.NewReader(""), &output, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 0 {
		t.Fatalf("failed jobs = %d, output = %q", result.Failed, output.String())
	}
}

func TestRunOnceLockedUsesNeutralTriggerLabel(t *testing.T) {
	stateDir := t.TempDir()
	release, err := acquireLock(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	cfg := Config{Jobs: []Job{{Name: "user-job", Scope: "user", Command: ":"}}}
	var output strings.Builder
	if _, err := runOnceLocked(cfg, stateDir, "startup", false, strings.NewReader(""), &output, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Started update run at ") {
		t.Fatalf("output = %q, want neutral start message", output.String())
	}
	if strings.Contains(output.String(), "terminal startup") {
		t.Fatalf("output = %q, want no terminal-startup label", output.String())
	}
}

func TestRunOnceLockedRefreshesSudoCredentialsForEachSystemJob(t *testing.T) {
	root := t.TempDir()
	fakeSudo := filepath.Join(root, "sudo")
	callsFile := filepath.Join(root, "calls")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$UPKEEP_TEST_SUDO_CALLS\"\n" +
		"if [ \"$1\" = \"-v\" ]; then exit 0; fi\n" +
		"if [ \"$1\" = \"-n\" ]; then shift; exec \"$@\"; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(fakeSudo, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UPKEEP_TEST_SUDO_CALLS", callsFile)
	originalSudoPath := sudoPath
	sudoPath = func() string { return fakeSudo }
	defer func() { sudoPath = originalSudoPath }()

	stateDir := filepath.Join(root, "state")
	release, err := acquireLock(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	cfg := Config{Jobs: []Job{
		{Name: "first-system-job", Scope: "system", Command: ":"},
		{Name: "second-system-job", Scope: "system", Command: ":"},
	}}
	var output strings.Builder
	result, err := runOnceLocked(cfg, stateDir, "manual", true, strings.NewReader(""), &output, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 0 {
		t.Fatalf("failed jobs = %d, output = %q", result.Failed, output.String())
	}
	calls, err := os.ReadFile(callsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(calls), "-v\n"); got != 2 {
		t.Fatalf("sudo -v calls = %d, calls = %q; want one per system job", got, calls)
	}
}

func TestRunDueCommandRunsAfterApproval(t *testing.T) {
	configDir := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	configPath := filepath.Join(configDir, "upkeep.toml")
	if err := os.WriteFile(configPath, []byte(`
interval = "24h"
snooze = "24h"

[[jobs]]
name = "user-packages"
scope = "user"
command = "printf 'startup update'"
`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}

	var output, errorsOutput strings.Builder
	code := runDueCommand(cfg, stateDir, 24*time.Hour, false, 24*time.Hour, 60*time.Second, false, true, strings.NewReader("y\n"), &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q", code, errorsOutput.String())
	}
	if !strings.Contains(output.String(), "startup update") {
		t.Fatalf("output = %q, want startup command output", output.String())
	}
	if strings.Contains(output.String(), "=") {
		t.Fatalf("output = %q, want human-readable output", output.String())
	}
	state, err := loadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !state.LastPrompt.IsZero() || state.LastSuccess.IsZero() {
		t.Fatalf("startup state = %+v, want no snooze after approval", state)
	}
}

func TestRunDueCommandDoesNotSnoozeAfterFailedApproval(t *testing.T) {
	stateDir := t.TempDir()
	cfg := Config{Jobs: []Job{{Name: "user-packages", Scope: "user", Command: "false"}}}
	var output, errorsOutput strings.Builder
	code := runDueCommand(cfg, stateDir, 0, true, 24*time.Hour, 60*time.Second, false, true, strings.NewReader("y\n"), &output, &errorsOutput)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; errors = %q", code, errorsOutput.String())
	}
	state, err := loadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !state.LastPrompt.IsZero() {
		t.Fatalf("last prompt = %v, want no snooze after failed run", state.LastPrompt)
	}
	if state.LastResultByJob["user-packages"] != jobResultFailed {
		t.Fatalf("job result = %q, want failed", state.LastResultByJob["user-packages"])
	}
}

func TestRunDueCommandTimesOutWithoutSnoozing(t *testing.T) {
	stateDir := t.TempDir()
	cfg := Config{Jobs: []Job{{Name: "user-packages", Scope: "user", Command: "false"}}}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	var output, errorsOutput strings.Builder
	code := runDueCommand(cfg, stateDir, 0, true, 24*time.Hour, 10*time.Millisecond, false, true, reader, &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q", code, errorsOutput.String())
	}
	if !strings.Contains(errorsOutput.String(), "approval timed out") {
		t.Fatalf("errors = %q, want timeout message", errorsOutput.String())
	}
	state, err := loadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !state.LastPrompt.IsZero() || !state.LastAttempt.IsZero() {
		t.Fatalf("state = %+v, want no prompt or run recorded", state)
	}
	release, err := acquireLock(stateDir)
	if err != nil {
		t.Fatalf("acquire lock after timeout: %v", err)
	}
	release()
}

func TestRunDueCommandWithZeroIntervalStillAsksForApproval(t *testing.T) {
	stateDir := t.TempDir()
	cfg := Config{Jobs: []Job{{Name: "user-packages", Scope: "user", Command: ":"}}}
	var output, errorsOutput strings.Builder
	code := runDueCommand(cfg, stateDir, 0, true, 24*time.Hour, 60*time.Second, false, true, strings.NewReader("n\n"), &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q", code, errorsOutput.String())
	}
	if !strings.Contains(output.String(), "updates are due") {
		t.Fatalf("output = %q, want approval prompt", output.String())
	}
	state, err := loadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastPrompt.IsZero() || !state.LastSuccess.IsZero() {
		t.Fatalf("state = %+v, want prompt without a run", state)
	}
}

func TestRunDueCommandRejectsSystemJobsWithoutInteractiveTerminal(t *testing.T) {
	stateDir := t.TempDir()
	cfg := Config{Jobs: []Job{
		{Name: "user", Scope: "user", Command: ":"},
		{Name: "system", Scope: "system", Command: ":"},
	}}
	var output, errorsOutput strings.Builder
	code := runDueCommand(cfg, stateDir, 0, true, 24*time.Hour, 60*time.Second, true, false, strings.NewReader(""), &output, &errorsOutput)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errorsOutput.String(), "system jobs require an interactive terminal") {
		t.Fatalf("errors = %q, want system job preflight error", errorsOutput.String())
	}
	state, err := loadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !state.LastAttempt.IsZero() {
		t.Fatalf("state = %+v, want no run attempt after preflight failure", state)
	}
}

func TestRunDueCommandPreservesCommandInputAfterApproval(t *testing.T) {
	configDir := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	configPath := filepath.Join(configDir, "upkeep.toml")
	if err := os.WriteFile(configPath, []byte(`
interval = "24h"
snooze = "24h"

[[jobs]]
name = "interactive-job"
scope = "user"
command = "read value && printf 'received %s' \"$value\""
`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}

	var output, errorsOutput strings.Builder
	code := runDueCommand(cfg, stateDir, 24*time.Hour, false, 24*time.Hour, 60*time.Second, false, true, strings.NewReader("y\ncommand input\n"), &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q, output = %q", code, errorsOutput.String(), output.String())
	}
	if !strings.Contains(output.String(), "received command input") {
		t.Fatalf("output = %q, want command input after approval", output.String())
	}
}

func TestRunDueCommandRunsOnlyDueJobs(t *testing.T) {
	stateDir := t.TempDir()
	oldUserSuccess := time.Now().Add(-25 * time.Hour)
	recentSystemSuccess := time.Now().Add(-time.Hour)
	if err := saveState(stateDir, State{LastSuccessByJob: map[string]time.Time{
		"user":   oldUserSuccess,
		"system": recentSystemSuccess,
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Jobs: []Job{
			{Name: "user", Scope: "user", Interval: "24h", Command: "printf user-update"},
			{Name: "system", Scope: "system", Interval: "720h", Command: "printf system-update"},
		},
	}
	var output, errorsOutput strings.Builder
	code := runDueCommand(cfg, stateDir, 168*time.Hour, false, 24*time.Hour, 60*time.Second, true, true, strings.NewReader(""), &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q, output = %q", code, errorsOutput.String(), output.String())
	}
	if !strings.Contains(output.String(), "user-update") {
		t.Fatalf("output = %q, want user job output", output.String())
	}
	if strings.Contains(output.String(), "system-update") {
		t.Fatalf("output = %q, want system job to remain skipped", output.String())
	}
	state, err := loadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !state.LastSuccessByJob["user"].After(oldUserSuccess) {
		t.Fatalf("user last success = %v, want it updated", state.LastSuccessByJob["user"])
	}
	if !state.LastSuccessByJob["system"].Equal(recentSystemSuccess) {
		t.Fatalf("system last success = %v, want %v", state.LastSuccessByJob["system"], recentSystemSuccess)
	}
	if state.LastResultByJob["user"] != jobResultSucceeded {
		t.Fatalf("user job result = %q, want succeeded", state.LastResultByJob["user"])
	}
	if state.LastResultByJob["system"] != jobResultNotRun {
		t.Fatalf("system job result = %q, want not run", state.LastResultByJob["system"])
	}
}

func TestRunDueCommandIntervalOverrideAppliesToEveryJob(t *testing.T) {
	stateDir := t.TempDir()
	if err := saveState(stateDir, State{LastSuccessByJob: map[string]time.Time{
		"system": time.Now().Add(-48 * time.Hour),
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Jobs: []Job{{
		Name:     "system",
		Scope:    "user",
		Interval: "720h",
		Command:  "printf overridden",
	}}}
	var output, errorsOutput strings.Builder
	code := runDueCommand(cfg, stateDir, 24*time.Hour, true, 24*time.Hour, 60*time.Second, true, true, strings.NewReader(""), &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q, output = %q", code, errorsOutput.String(), output.String())
	}
	if !strings.Contains(output.String(), "overridden") {
		t.Fatalf("output = %q, want the global interval override to apply", output.String())
	}
}

func TestDueJobsUsesGroupIntervalsAndJobOrder(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	cfg := Config{
		Jobs: []Job{
			{Name: "daily", Scope: "user", Command: ":"},
			{Name: "reports-first", Scope: "user", Command: ":"},
			{Name: "reports-second", Scope: "user", Command: ":"},
		},
		Groups: []Group{
			{Name: "reports", Interval: "720h", Jobs: []string{"reports-first", "reports-second"}},
			{Name: "default", Interval: "24h", Jobs: []string{"daily"}},
		},
	}
	state := State{LastSuccessByJob: map[string]time.Time{
		"daily":          now.Add(-25 * time.Hour),
		"reports-first":  now.Add(-721 * time.Hour),
		"reports-second": now.Add(-719 * time.Hour),
	}}

	due, err := dueJobs(cfg, state, 168*time.Hour, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("due jobs = %+v, want daily and reports-first", due)
	}
	if due[0].Name != "daily" || due[1].Name != "reports-first" {
		t.Fatalf("due job order = %q, %q, want daily, reports-first", due[0].Name, due[1].Name)
	}
}

func TestDueJobsIntervalOverrideAppliesToEveryJob(t *testing.T) {
	now := time.Now()
	cfg := Config{
		Jobs: []Job{
			{Name: "daily", Scope: "user", Command: ":"},
			{Name: "reports", Scope: "user", Command: ":"},
		},
		Groups: []Group{
			{Name: "default", Interval: "720h", Jobs: []string{"daily"}},
			{Name: "reports", Interval: "720h", Jobs: []string{"reports"}},
		},
	}
	state := State{LastSuccessByJob: map[string]time.Time{
		"daily":   now.Add(-time.Hour),
		"reports": now.Add(-time.Hour),
	}}

	due, err := dueJobs(cfg, state, 0, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("due jobs = %+v, want both group jobs", due)
	}
}

func TestRunOnceLockedExecutesJobsInConfigOrder(t *testing.T) {
	stateDir := t.TempDir()
	release, err := acquireLock(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	cfg := Config{
		Jobs: []Job{
			{Name: "daily", Scope: "user", Command: "printf daily"},
			{Name: "reports-first", Scope: "user", Command: "printf reports-first"},
			{Name: "reports-second", Scope: "user", Command: "printf reports-second"},
		},
		Groups: []Group{
			{Name: "reports", Jobs: []string{"reports-first", "reports-second"}},
			{Name: "default", Jobs: []string{"daily"}},
		},
	}
	var output strings.Builder
	if _, err := runOnceLocked(cfg, stateDir, "manual", false, strings.NewReader(""), &output, nil); err != nil {
		t.Fatal(err)
	}

	last := -1
	for _, name := range []string{"daily", "reports-first", "reports-second"} {
		index := strings.Index(output.String(), `Running user job "`+name+`".`)
		if index <= last {
			t.Fatalf("output = %q, want %s after previous job", output.String(), name)
		}
		last = index
	}
}

func TestDryRunReportsDueAndNotDueJobsWithoutSideEffects(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	marker := filepath.Join(root, "marker")
	recent := time.Now().Add(-time.Hour)
	if err := saveState(stateDir, State{LastSuccessByJob: map[string]time.Time{
		"daily":   time.Now().Add(-25 * time.Hour),
		"monthly": recent,
		"weekly":  time.Now().Add(-8 * 24 * time.Hour),
	}}); err != nil {
		t.Fatal(err)
	}
	stateBefore, err := os.ReadFile(filepath.Join(stateDir, "state.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Jobs: []Job{
		{Name: "daily", Scope: "user", Interval: "24h", Command: "touch " + marker},
		{Name: "monthly", Scope: "user", Interval: "720h", Command: "touch " + marker + "-monthly"},
		{Name: "weekly", Scope: "user", Interval: "168h", Command: "touch " + marker + "-weekly"},
	}}
	var output, errorsOutput strings.Builder
	code := dryRunCommand(cfg, stateDir, 168*time.Hour, false, &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q", code, errorsOutput.String())
	}
	if !strings.Contains(output.String(), "Updates are due.") || !strings.Contains(output.String(), "Dry run:\n") {
		t.Fatalf("output = %q, want the due job and result", output.String())
	}
	for _, want := range []string{
		"  daily (user) : due now\n    touch " + marker,
		"  monthly (user) : next due in ",
		"  weekly (user) : due now\n    touch " + marker + "-weekly",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output = %q, want %q", output.String(), want)
		}
	}
	if !strings.Contains(output.String(), "\n\n  monthly") || !strings.Contains(output.String(), "\n\n  weekly") {
		t.Fatalf("output = %q, want blank lines between jobs", output.String())
	}
	if !strings.Contains(output.String(), "  monthly (user) : next due in ") {
		t.Fatalf("output = %q, want next due time for non-due job", output.String())
	}
	if !strings.Contains(output.String(), "touch "+marker) {
		t.Fatalf("output = %q, want due command", output.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker stat error = %v, want command not to run", err)
	}
	stateAfter, err := os.ReadFile(filepath.Join(stateDir, "state.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stateAfter) != string(stateBefore) {
		t.Fatalf("state changed during dry-run:\nbefore: %s\nafter: %s", stateBefore, stateAfter)
	}
}

func TestDryRunReportsWhenUpdatesAreNotDue(t *testing.T) {
	stateDir := t.TempDir()
	if err := saveState(stateDir, State{LastSuccessByJob: map[string]time.Time{
		"daily": time.Now().Add(-time.Hour),
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Jobs: []Job{{Name: "daily", Scope: "user", Interval: "24h", Command: "printf should-not-run"}}}
	var output, errorsOutput strings.Builder
	code := dryRunCommand(cfg, stateDir, 168*time.Hour, false, &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q", code, errorsOutput.String())
	}
	if !strings.HasPrefix(output.String(), "Updates are not due.\n\nDry run:\n") {
		t.Fatalf("output = %q, want a not-due result", output.String())
	}
	if !strings.Contains(output.String(), "  daily (user) : next due in ") {
		t.Fatalf("output = %q, want next due time", output.String())
	}
}

func TestFormatDurationUntil(t *testing.T) {
	for _, test := range []struct {
		remaining time.Duration
		want      string
	}{
		{24 * time.Hour, "1d"},
		{12*time.Hour + time.Minute, "12h 1m"},
		{12 * time.Hour, "12h"},
		{90 * time.Minute, "1h 30m"},
		{25 * time.Hour, "1d 1h"},
		{30 * time.Second, "30s"},
		{1500 * time.Millisecond, "2s"},
	} {
		if got := formatDurationUntil(test.remaining); got != test.want {
			t.Fatalf("formatDurationUntil(%s) = %q, want %q", test.remaining, got, test.want)
		}
	}
}

func TestRunOnceLockedRecordsSkippedJob(t *testing.T) {
	stateDir := t.TempDir()
	release, err := acquireLock(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	cfg := Config{Jobs: []Job{{Name: "system", Scope: "system", Command: ":"}}}
	var output strings.Builder
	result, err := runOnceLocked(cfg, stateDir, "manual", false, strings.NewReader(""), &output, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 1 {
		t.Fatalf("failed jobs = %d, want 1", result.Failed)
	}
	state, err := loadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastResultByJob["system"] != jobResultSkipped {
		t.Fatalf("system job result = %q, want skipped", state.LastResultByJob["system"])
	}
}

func TestRunOutputColorsTerminalMessagesOnly(t *testing.T) {
	var logOutput, terminalOutput strings.Builder
	output := runOutput{log: &logOutput, terminal: &terminalOutput, color: true}
	output.message(ansiCyan, "Running job.\n")

	if !strings.Contains(terminalOutput.String(), ansiCyan) {
		t.Fatalf("terminal output = %q, want ANSI color", terminalOutput.String())
	}
	if strings.Contains(logOutput.String(), "\x1b[") {
		t.Fatalf("log output = %q, want no ANSI color", logOutput.String())
	}
}

func TestExecuteJobWithPTYPreservesTerminalOutputAndInput(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("pty is unavailable: %v", err)
	}
	defer master.Close()
	defer slave.Close()

	cmd := exec.Command("/bin/sh", "-c", `test -t 1 && read value && printf '\033[31mreceived %s\033[0m' "$value"`)
	var output strings.Builder
	done := make(chan error, 1)
	go func() {
		done <- executeJobWithPTY(cmd, slave, &output)
	}()

	if _, err := master.Write([]byte("pty input\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("executeJobWithPTY error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("executeJobWithPTY timed out")
	}
	if !strings.Contains(output.String(), "\x1b[31mreceived pty input\x1b[0m") {
		t.Fatalf("output = %q, want TTY color and input", output.String())
	}
}

func TestRunCLIWithYesAndZeroIntervalRunsImmediately(t *testing.T) {
	configDir := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	configPath := filepath.Join(configDir, "upkeep.toml")
	if err := os.WriteFile(configPath, []byte(`
interval = "24h"

[[jobs]]
name = "user-packages"
scope = "user"
command = "printf 'manual update'"
`), 0600); err != nil {
		t.Fatal(err)
	}

	var output, errorsOutput strings.Builder
	code := runCLI([]string{
		"run",
		"-y",
		"--interval=0",
		"--config", configPath,
		"--state-dir", stateDir,
	}, strings.NewReader(""), &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q", code, errorsOutput.String())
	}
	if !strings.Contains(output.String(), "manual update") {
		t.Fatalf("output = %q, want manual command output", output.String())
	}
	if strings.Contains(output.String(), "updates are due") {
		t.Fatalf("output = %q, want no approval prompt", output.String())
	}
}

func TestRunCLIGroupRunsOnlySelectedJobs(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	stateDir := filepath.Join(root, "state")
	if err := os.WriteFile(configPath, []byte(`
[[jobs]]
name = "mise"
command = "printf mise"

[[jobs]]
name = "apm"
command = "printf apm"

[[jobs]]
name = "other"
command = "printf other"

[[groups]]
name = "dev-tools"
jobs = ["mise", "apm"]

[[groups]]
name = "other-tools"
jobs = ["other"]
`), 0600); err != nil {
		t.Fatal(err)
	}

	var output, errorsOutput strings.Builder
	code := runCLI([]string{
		"run",
		"--group", "dev-tools",
		"--yes",
		"--interval=0",
		"--config", configPath,
		"--state-dir", stateDir,
	}, strings.NewReader(""), &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q, output = %q", code, errorsOutput.String(), output.String())
	}
	if !strings.Contains(output.String(), "mise") || !strings.Contains(output.String(), "apm") {
		t.Fatalf("output = %q, want selected group jobs", output.String())
	}
	if strings.Contains(output.String(), "other") {
		t.Fatalf("output = %q, want other group job excluded", output.String())
	}
	state, err := loadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastResultByJob["other"] != jobResultNotRun {
		t.Fatalf("other job result = %q, want not run", state.LastResultByJob["other"])
	}
}

func TestRunCLIRejectsUnknownGroup(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
[[jobs]]
name = "mise"
command = "true"

[[groups]]
name = "dev-tools"
jobs = ["mise"]
`), 0600); err != nil {
		t.Fatal(err)
	}

	var output, errorsOutput strings.Builder
	code := runCLI([]string{
		"run",
		"--group", "missing",
		"--dry-run",
		"--config", configPath,
		"--state-dir", filepath.Join(root, "state"),
	}, strings.NewReader(""), &output, &errorsOutput)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errorsOutput.String(), `unknown group "missing"`) {
		t.Fatalf("errors = %q, want unknown group error", errorsOutput.String())
	}
}

func TestIsTerminalRejectsDeviceFile(t *testing.T) {
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	if isTerminal(file) {
		t.Fatalf("isTerminal(%s) = true, want false", os.DevNull)
	}
}

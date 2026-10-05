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

[[workflows]]
name = "dev-tools"
interval = "24h"
[[workflows.jobs]]
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
	result, err := runOnceLocked(cfg, stateDir, "manual", false, strings.NewReader(""), &output)
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
	if state.workflow("dev-tools").LastSuccess.IsZero() || state.workflow("dev-tools").LastFinished.IsZero() {
		t.Fatalf("state timestamps were not written: %+v", state)
	}
	if state.workflow("dev-tools").LastResultByJob["user-packages"] != jobResultSucceeded {
		t.Fatalf("user job result = %q, want succeeded", state.workflow("dev-tools").LastResultByJob["user-packages"])
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
	t.Setenv("UPKEEP_TEST_STATE_FILE", filepath.Join(stateDir, "workflows.toml"))
	cfg := Config{Workflows: []Workflow{{Name: "dev-tools", Interval: "24h", Jobs: []Job{
		{Name: "first-job", Scope: "user", Command: ":"},
		{Name: "second-job", Scope: "user", Command: `grep -F 'first-job = "succeeded"' "$UPKEEP_TEST_STATE_FILE" >/dev/null`},
	}}}}
	release, err := acquireLock(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	var output strings.Builder
	result, err := runOnceLocked(cfg, stateDir, "manual", false, strings.NewReader(""), &output)
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

	cfg := Config{Workflows: []Workflow{{Name: "dev-tools", Interval: "24h", Jobs: []Job{{Name: "user-job", Scope: "user", Command: ":"}}}}}
	var output strings.Builder
	if _, err := runOnceLocked(cfg, stateDir, "startup", false, strings.NewReader(""), &output); err != nil {
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

	cfg := Config{Workflows: []Workflow{{Name: "dev-tools", Interval: "24h", Jobs: []Job{
		{Name: "first-system-job", Scope: "system", Command: ":"},
		{Name: "second-system-job", Scope: "system", Command: ":"},
	}}}}
	var output strings.Builder
	result, err := runOnceLocked(cfg, stateDir, "manual", true, strings.NewReader(""), &output)
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
snooze = "24h"

[[workflows]]
name = "dev-tools"
interval = "24h"
[[workflows.jobs]]
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
	code := runDueCommand(cfg, stateDir, false, 24*time.Hour, 60*time.Second, false, true, strings.NewReader("y\n"), &output, &errorsOutput)
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
	if !state.workflow("dev-tools").LastPrompt.IsZero() || state.workflow("dev-tools").LastSuccess.IsZero() {
		t.Fatalf("startup state = %+v, want no snooze after approval", state)
	}
}

func TestRunDueCommandDoesNotSnoozeAfterFailedApproval(t *testing.T) {
	stateDir := t.TempDir()
	cfg := Config{Workflows: []Workflow{{Name: "dev-tools", Interval: "24h", Jobs: []Job{{Name: "user-packages", Scope: "user", Command: "false"}}}}}
	var output, errorsOutput strings.Builder
	code := runDueCommand(cfg, stateDir, true, 24*time.Hour, 60*time.Second, false, true, strings.NewReader("y\n"), &output, &errorsOutput)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; errors = %q", code, errorsOutput.String())
	}
	state, err := loadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !state.workflow("dev-tools").LastPrompt.IsZero() {
		t.Fatalf("last prompt = %v, want no snooze after failed run", state.workflow("dev-tools").LastPrompt)
	}
	if state.workflow("dev-tools").LastResultByJob["user-packages"] != jobResultFailed {
		t.Fatalf("job result = %q, want failed", state.workflow("dev-tools").LastResultByJob["user-packages"])
	}
}

func TestRunDueCommandTimesOutWithoutSnoozing(t *testing.T) {
	stateDir := t.TempDir()
	cfg := Config{Workflows: []Workflow{{Name: "dev-tools", Interval: "24h", Jobs: []Job{{Name: "user-packages", Scope: "user", Command: "false"}}}}}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	var output, errorsOutput strings.Builder
	code := runDueCommand(cfg, stateDir, true, 24*time.Hour, 10*time.Millisecond, false, true, reader, &output, &errorsOutput)
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
	if !state.workflow("dev-tools").LastPrompt.IsZero() || !state.workflow("dev-tools").LastAttempt.IsZero() {
		t.Fatalf("state = %+v, want no prompt or run recorded", state)
	}
	release, err := acquireLock(stateDir)
	if err != nil {
		t.Fatalf("acquire lock after timeout: %v", err)
	}
	release()
}

func TestRunDueCommandWithForceStillAsksForApproval(t *testing.T) {
	stateDir := t.TempDir()
	cfg := Config{Workflows: []Workflow{{Name: "dev-tools", Interval: "24h", Jobs: []Job{{Name: "user-packages", Scope: "user", Command: ":"}}}}}
	var output, errorsOutput strings.Builder
	code := runDueCommand(cfg, stateDir, true, 24*time.Hour, 60*time.Second, false, true, strings.NewReader("n\n"), &output, &errorsOutput)
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
	if state.workflow("dev-tools").LastPrompt.IsZero() || !state.workflow("dev-tools").LastSuccess.IsZero() {
		t.Fatalf("state = %+v, want prompt without a run", state)
	}
}

func TestRunDueCommandRejectsSystemJobsWithoutInteractiveTerminal(t *testing.T) {
	stateDir := t.TempDir()
	cfg := Config{Workflows: []Workflow{{Name: "dev-tools", Interval: "24h", Jobs: []Job{
		{Name: "user", Scope: "user", Command: ":"},
		{Name: "system", Scope: "system", Command: ":"},
	}}}}
	var output, errorsOutput strings.Builder
	code := runDueCommand(cfg, stateDir, true, 24*time.Hour, 60*time.Second, true, false, strings.NewReader(""), &output, &errorsOutput)
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
	if !state.workflow("dev-tools").LastAttempt.IsZero() {
		t.Fatalf("state = %+v, want no run attempt after preflight failure", state)
	}
}

func TestRunDueCommandPreservesCommandInputAfterApproval(t *testing.T) {
	configDir := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	configPath := filepath.Join(configDir, "upkeep.toml")
	if err := os.WriteFile(configPath, []byte(`
snooze = "24h"

[[workflows]]
name = "dev-tools"
interval = "24h"
[[workflows.jobs]]
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
	code := runDueCommand(cfg, stateDir, false, 24*time.Hour, 60*time.Second, false, true, strings.NewReader("y\ncommand input\n"), &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q, output = %q", code, errorsOutput.String(), output.String())
	}
	if !strings.Contains(output.String(), "received command input") {
		t.Fatalf("output = %q, want command input after approval", output.String())
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

	cfg := Config{Workflows: []Workflow{{Name: "dev-tools", Interval: "24h", Jobs: []Job{{Name: "system", Scope: "system", Command: ":"}}}}}
	var output strings.Builder
	result, err := runOnceLocked(cfg, stateDir, "manual", false, strings.NewReader(""), &output)
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
	if state.workflow("dev-tools").LastResultByJob["system"] != jobResultSkipped {
		t.Fatalf("system job result = %q, want skipped", state.workflow("dev-tools").LastResultByJob["system"])
	}
	due, err := dueWorkflows(cfg, state, false, time.Now())
	if err != nil || len(due) != 0 {
		t.Fatalf("skipped job did not advance workflow cadence: due=%v err=%v", due, err)
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

func TestRunCLIWithYesAndForceRunsImmediately(t *testing.T) {
	configDir := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	configPath := filepath.Join(configDir, "upkeep.toml")
	if err := os.WriteFile(configPath, []byte(`

[[workflows]]
name = "dev-tools"
interval = "24h"
[[workflows.jobs]]
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
		"--force",
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

func workflowConfig(jobs ...Job) Config {
	return Config{Workflows: []Workflow{{Name: "dev-tools", Interval: "24h", Jobs: jobs}}}
}

func TestWorkflowsMixedDueFailureCadenceAndNames(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	recent := now.Add(-time.Hour)
	if err := saveState(dir, State{Workflows: map[string]*WorkflowState{"monthly": {LastFinished: recent, LastResultByJob: map[string]string{"shared": jobResultSucceeded}}}}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Workflows: []Workflow{
		{Name: "daily", Interval: "24h", Jobs: []Job{{Name: "shared", Command: "exit 7"}, {Name: "later", Command: "printf later-job"}}},
		{Name: "monthly", Interval: "720h", Jobs: []Job{{Name: "shared", Command: "printf NOT-DUE"}}},
		{Name: "weekly", Interval: "168h", Jobs: []Job{{Name: "shared", Command: "printf later-workflow"}}},
	}}
	var out, errs strings.Builder
	if code := runDueCommand(cfg, dir, false, 0, time.Second, true, false, nil, &out, &errs); code != 1 {
		t.Fatalf("code=%d errors=%s", code, errs.String())
	}
	if !strings.Contains(out.String(), "later-job") || !strings.Contains(out.String(), "later-workflow") || strings.Contains(out.String(), "NOT-DUE") {
		t.Fatal(out.String())
	}
	state, err := loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"daily", "weekly"} {
		history := state.Workflows[name]
		if history.LastAttempt.IsZero() || history.LastFinished.Before(history.LastAttempt) {
			t.Fatalf("invalid history: %+v", history)
		}
		next, err := nextWorkflowDue(state, cfg.Workflows[map[string]int{"daily": 0, "weekly": 2}[name]])
		interval, _ := cfg.Workflows[map[string]int{"daily": 0, "weekly": 2}[name]].duration()
		if err != nil || !next.Equal(history.LastFinished.Add(interval)) {
			t.Fatalf("next=%v err=%v", next, err)
		}
	}
	if state.Workflows["daily"].LastResultByJob["shared"] != jobResultFailed || state.Workflows["weekly"].LastResultByJob["shared"] != jobResultSucceeded {
		t.Fatalf("colliding results: %+v", state)
	}
	if !state.Workflows["monthly"].LastFinished.Equal(recent) {
		t.Fatal("non-due workflow changed")
	}
	if state.Workflows["daily"].LastExitCode != 1 || !state.Workflows["daily"].LastSuccess.IsZero() {
		t.Fatal("failure history incorrect")
	}
	due, err := dueWorkflows(cfg, state, false, time.Now())
	if err != nil || len(due) != 0 {
		t.Fatalf("due=%v err=%v", due, err)
	}
	due, err = dueWorkflows(cfg, state, true, time.Now())
	if err != nil || len(due) != 3 {
		t.Fatalf("forced=%v err=%v", due, err)
	}
	// Failure must preserve the last successful workflow timestamp.
	success := now.Add(-48 * time.Hour)
	state.Workflows["daily"].LastSuccess = success
	if err := saveState(dir, state); err != nil {
		t.Fatal(err)
	}
	cfg.Workflows = cfg.Workflows[:1]
	if _, err := runOnceLocked(cfg, dir, "test", false, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	state, err = loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Workflows["daily"].LastSuccess.Equal(success) {
		t.Fatal("failure erased last success")
	}
}

func TestWorkflowSnoozeForceAndYes(t *testing.T) {
	dir := t.TempDir()
	cfg := workflowConfig(Job{Name: "job", Command: "true"})
	var out, errs strings.Builder
	if code := runDueCommand(cfg, dir, true, 24*time.Hour, time.Second, false, true, strings.NewReader("n\n"), &out, &errs); code != 0 {
		t.Fatal(errs.String())
	}
	state, err := loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	history := state.Workflows["dev-tools"]
	if history.LastPrompt.IsZero() || !history.LastAttempt.IsZero() || !history.LastFinished.IsZero() {
		t.Fatalf("decline changed deadlines: %+v", history)
	}
	out.Reset()
	if code := runDueCommand(cfg, dir, true, 24*time.Hour, time.Second, false, true, strings.NewReader("y\n"), &out, &errs); code != 0 || out.Len() != 0 {
		t.Fatalf("force bypassed snooze: %s %s", out.String(), errs.String())
	}
	if code := runDueCommand(cfg, dir, false, 24*time.Hour, time.Second, true, false, nil, &out, &errs); code != 0 {
		t.Fatal(errs.String())
	}
	state, err = loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Workflows["dev-tools"].LastFinished.IsZero() {
		t.Fatal("yes did not bypass snooze")
	}
}

func TestWorkflowIndependentCheckpointAndIncompleteDeadline(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPKEEP_TEST_STATE_FILE", filepath.Join(dir, "workflows.toml"))
	cfg := Config{Workflows: []Workflow{
		{Name: "first", Interval: "24h", Jobs: []Job{{Name: "same", Command: "true"}}},
		{Name: "second", Interval: "24h", Jobs: []Job{{Name: "same", Command: `grep -F 'last_finished = 20' "$UPKEEP_TEST_STATE_FILE" >/dev/null`}, {Name: "after", Command: "true"}}},
	}}
	if _, err := runOnceLocked(cfg, dir, "test", false, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Workflows["second"].LastResultByJob["same"] != jobResultSucceeded {
		t.Fatal("first workflow completion was not checkpointed before second workflow started")
	}
	first := *state.Workflows["first"]
	// Emulate an interrupted subsequent run after its initial checkpoint.
	state.Workflows["second"].LastAttempt = time.Now().Add(time.Second)
	state.Workflows["second"].LastExitCode = incompleteExitCode
	state.Workflows["second"].LastResultByJob["same"] = jobResultIncomplete
	if err := saveState(dir, state); err != nil {
		t.Fatal(err)
	}
	state, err = loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	due, err := dueWorkflows(cfg, state, false, time.Now())
	if err != nil || len(due) != 1 || due[0].Name != "second" {
		t.Fatalf("due=%v err=%v", due, err)
	}
	if !state.Workflows["first"].LastFinished.Equal(first.LastFinished) || state.Workflows["first"].LastResultByJob["same"] != jobResultSucceeded {
		t.Fatal("completed workflow erased")
	}
}

func TestSignaledJobContinuesWorkflow(t *testing.T) {
	dir := t.TempDir()
	cfg := workflowConfig(Job{Name: "signal", Command: "kill -TERM $$"}, Job{Name: "after", Command: "true"})
	result, err := runOnceLocked(cfg, dir, "test", false, nil, io.Discard)
	if err != nil || result.Failed != 1 {
		t.Fatalf("result=%v err=%v", result, err)
	}
	state, err := loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	history := state.Workflows["dev-tools"]
	if history.LastFinished.IsZero() || history.LastResultByJob["after"] != jobResultSucceeded {
		t.Fatalf("history=%+v", history)
	}
}

func TestWorkflowDryRunNoSideEffects(t *testing.T) {
	dir := t.TempDir()
	recent := time.Now().Add(-time.Hour)
	state := State{Workflows: map[string]*WorkflowState{"later": {LastFinished: recent}}}
	if err := saveState(dir, state); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "workflows.toml"))
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "marker")
	cfg := Config{Workflows: []Workflow{
		{Name: "due", Interval: "24h", Jobs: []Job{{Name: "job", Scope: "user", Command: "touch " + marker}}},
		{Name: "later", Interval: "24h", Jobs: []Job{{Name: "job", Scope: "system", Command: "touch " + marker}}},
	}}
	var out, errs strings.Builder
	if code := dryRunCommand(cfg, dir, false, &out, &errs); code != 0 {
		t.Fatal(errs.String())
	}
	if !strings.Contains(out.String(), "due : due now") || !strings.Contains(out.String(), "later : next due in") {
		t.Fatal(out.String())
	}
	after, err := os.ReadFile(filepath.Join(dir, "workflows.toml"))
	if err != nil || string(before) != string(after) {
		t.Fatal("dry run altered state")
	}
	for _, path := range []string{marker, filepath.Join(dir, "lock"), filepath.Join(dir, "logs")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("side effect: %s", path)
		}
	}
}

func TestWorkflowExactDeadlineBoundary(t *testing.T) {
	finish := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	workflow := Workflow{Name: "daily", Interval: "24h"}
	state := State{Workflows: map[string]*WorkflowState{"daily": {LastAttempt: finish.Add(-time.Minute), LastFinished: finish, LastExitCode: 1}}}
	cfg := Config{Workflows: []Workflow{workflow}}
	for _, test := range []struct {
		now   time.Time
		count int
	}{
		{finish.Add(24*time.Hour - time.Nanosecond), 0},
		{finish.Add(24 * time.Hour), 1},
	} {
		due, err := dueWorkflows(cfg, state, false, test.now)
		if err != nil || len(due) != test.count {
			t.Fatalf("at=%v due=%v error=%v", test.now, due, err)
		}
	}
}

func TestDecliningWorkflowDoesNotSnoozeOtherWorkflows(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Workflows: []Workflow{
		{Name: "A", Interval: "24h", Jobs: []Job{{Name: "job", Command: "true"}}},
		{Name: "B", Interval: "24h", Jobs: []Job{{Name: "job", Command: "true"}}},
	}}
	selected, err := cfg.selectWorkflows([]string{"A"})
	if err != nil {
		t.Fatal(err)
	}
	var out, errs strings.Builder
	if code := runDueCommand(selected, dir, false, 24*time.Hour, time.Second, false, true, strings.NewReader("n\n"), &out, &errs); code != 0 {
		t.Fatal(errs.String())
	}
	out.Reset()
	if code := runDueCommand(cfg, dir, false, 24*time.Hour, time.Second, false, true, strings.NewReader("y\n"), &out, &errs); code != 0 {
		t.Fatal(errs.String())
	}
	state, err := loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Workflows["A"].LastAttempt.IsZero() || state.Workflows["B"].LastSuccess.IsZero() {
		t.Fatalf("snooze crossed workflows: %+v", state)
	}
	if !strings.Contains(out.String(), "updates are due") {
		t.Fatal("B approval was suppressed")
	}
}

func TestWorkflowNamesWithSeparatorsDoNotCollide(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Workflows: []Workflow{
		{Name: "a.b", Interval: "24h", Jobs: []Job{{Name: "c", Command: "false"}}},
		{Name: "a", Interval: "24h", Jobs: []Job{{Name: "b.c", Command: "true"}}},
	}}
	if _, err := runOnceLocked(cfg, dir, "test", false, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Workflows["a.b"].LastResultByJob["c"] != jobResultFailed || state.Workflows["a"].LastResultByJob["b.c"] != jobResultSucceeded {
		t.Fatal("ambiguous names collided")
	}
}

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func retryConfigPath(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func retryState(t *testing.T, success, failure time.Time) State {
	t.Helper()
	var state State
	contents := fmt.Sprintf("[last_success_by_job]\njob = %s\n[last_failure_by_job]\njob = %s\n", success.Format(time.RFC3339Nano), failure.Format(time.RFC3339Nano))
	if _, err := toml.Decode(contents, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestRetryIntervalPrecedenceAndFallback(t *testing.T) {
	failedAt := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name, global, group, job string
		want                     time.Duration
	}{
		{"job", `retry_interval = "3h"`, `retry_interval = "2h"`, `retry_interval = "1h"`, time.Hour},
		{"group", `retry_interval = "3h"`, `retry_interval = "2h"`, "", 2 * time.Hour},
		{"global", `retry_interval = "3h"`, "", "", 3 * time.Hour},
		{"global before job normal", `retry_interval = "3h"`, "", `interval = "12h"`, 3 * time.Hour},
		{"group normal fallback", "", "", "", 24 * time.Hour},
		{"job normal fallback", "", "", `interval = "12h"`, 12 * time.Hour},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := retryConfigPath(t, fmt.Sprintf("interval = \"168h\"\n%s\n[[jobs]]\nname = \"job\"\ncommand = \"true\"\n%s\n[[groups]]\nname = \"group\"\njobs = [\"job\"]\ninterval = \"24h\"\n%s\n", tt.global, tt.job, tt.group))
			cfg, err := loadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, success := range []time.Time{{}, failedAt.Add(-time.Minute), failedAt.Add(-720 * time.Hour)} {
				state := retryState(t, success, failedAt)
				for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
					due, err := dueJobs(cfg, state, 168*time.Hour, false, failedAt.Add(tt.want+offset))
					if err != nil {
						t.Fatal(err)
					}
					if (len(due) == 1) != (offset >= 0) {
						t.Fatalf("success %v, deadline %+v: due = %v", success, offset, due)
					}
				}
			}
		})
	}
}

func TestRetryIntervalFallbackWithoutGroup(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, interval := range []time.Duration{48 * time.Hour, defaultInterval} {
		cfg := Config{Jobs: []Job{{Name: "job", Command: "true"}}}
		state := retryState(t, at.Add(-720*time.Hour), at)
		due, err := dueJobs(cfg, state, interval, false, at.Add(interval-time.Nanosecond))
		if err != nil {
			t.Fatal(err)
		}
		if len(due) != 0 {
			t.Fatalf("fallback %s: due = %v", interval, due)
		}
	}
}

func TestRetryIntervalRejectsInvalidSettings(t *testing.T) {
	for _, level := range []string{"global", "group", "job"} {
		for _, value := range []string{"bad", "0s", "-1h"} {
			t.Run(level+"/"+value, func(t *testing.T) {
				setting := fmt.Sprintf("retry_interval = %q\n", value)
				global, group, job := "", "", ""
				switch level {
				case "global":
					global = setting
				case "group":
					group = setting
				case "job":
					job = setting
				}
				path := retryConfigPath(t, global+"[[jobs]]\nname = \"job\"\ncommand = \"true\"\n"+job+"[[groups]]\nname = \"group\"\njobs = [\"job\"]\n"+group)
				_, err := loadConfig(path)
				if err == nil || !strings.Contains(err.Error(), "retry_interval") || strings.Contains(err.Error(), "unknown config") {
					t.Fatalf("error = %v, want invalid retry duration", err)
				}
			})
		}
	}
}

func TestRetryFailurePersistenceAndRecovery(t *testing.T) {
	stateDir := t.TempDir()
	oldSuccess := time.Now().Add(-720 * time.Hour).UTC()
	if err := saveState(stateDir, State{LastSuccess: oldSuccess, LastSuccessByJob: map[string]time.Time{"job": oldSuccess}}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Jobs: []Job{{Name: "job", Scope: "user", Command: "false"}}}
	started := time.Now()
	result, err := runOnceLocked(cfg, stateDir, "test", false, strings.NewReader(""), io.Discard, nil)
	if err != nil || result.Failed != 1 {
		t.Fatalf("result = %v, error = %v", result, err)
	}
	state, err := loadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !state.LastSuccess.Equal(oldSuccess) || !state.LastSuccessByJob["job"].Equal(oldSuccess) {
		t.Fatalf("failure changed success: %+v", state)
	}
	failedAt := state.LastFailureByJob["job"]
	if failedAt.Before(started) || failedAt.After(state.LastFinished) {
		t.Fatalf("failure completion = %v, run = %v..%v", failedAt, started, state.LastFinished)
	}
	// A later run of another job must retain the failed job's cooldown.
	cfg.Jobs = append(cfg.Jobs, Job{Name: "other", Scope: "user", Command: "true"})
	if _, err := runOnceLocked(cfg, stateDir, "test", false, nil, io.Discard, map[string]bool{"other": true}); err != nil {
		t.Fatal(err)
	}
	state, err = loadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	due, err := dueJobs(cfg, state, 24*time.Hour, false, failedAt.Add(time.Hour))
	if err != nil || len(due) != 0 {
		t.Fatalf("cooldown after another run: due = %v, error = %v", due, err)
	}
	cfg.Jobs[0].Command = "true"
	if _, err := runOnceLocked(cfg, stateDir, "test", false, nil, io.Discard, map[string]bool{"job": true}); err != nil {
		t.Fatal(err)
	}
	state, err = loadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := state.LastFailureByJob["job"]; exists {
		t.Fatalf("success retained failure: %v", state.LastFailureByJob)
	}
	if !state.LastSuccessByJob["job"].After(oldSuccess) {
		t.Fatal("success timestamp was not updated")
	}
	cfg.Jobs[0].RetryInterval = "1h"
	deadline, err := nextJobDue(state, cfg.Jobs[0], 24*time.Hour, false)
	if err != nil || !deadline.Equal(state.LastSuccessByJob["job"].Add(24*time.Hour)) {
		t.Fatalf("recovered deadline = %v, error = %v", deadline, err)
	}
}

func TestRetryDryRunAndCLIOverride(t *testing.T) {
	path := retryConfigPath(t, `interval = "168h"
retry_interval = "4h"
[[jobs]]
name = "job"
interval = "720h"
retry_interval = "2h"
command = "printf recovered"
[[groups]]
name = "group"
interval = "48h"
retry_interval = "3h"
jobs = ["job"]
`)
	for _, tt := range []struct {
		override string
		dry      bool
		want     string
	}{
		{"", true, "next due in 1h "},
		{"4h", true, "next due in 3h "},
		{"30m", true, "due now"},
		{"0", true, "due now"},
		{"", false, ""},
		{"4h", false, ""},
		{"30m", false, "recovered"},
		{"0", false, "recovered"},
	} {
		t.Run(fmt.Sprintf("%s/dry=%v", tt.override, tt.dry), func(t *testing.T) {
			stateDir := t.TempDir()
			now := time.Now().UTC()
			state := retryState(t, now.Add(-1000*time.Hour), now.Add(-time.Hour+time.Minute))
			if err := saveState(stateDir, state); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(stateDir, "state.toml"))
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"run", "--yes", "--group=group", "--config", path, "--state-dir", stateDir}
			if tt.dry {
				args = append(args, "--dry-run")
			}
			if tt.override != "" {
				args = append(args, "--interval="+tt.override)
			}
			var out, errOut strings.Builder
			if code := runCLI(args, strings.NewReader(""), &out, &errOut); code != 0 {
				t.Fatalf("exit = %d, errors = %s", code, errOut.String())
			}
			if tt.want == "" && out.Len() != 0 || !strings.Contains(out.String(), tt.want) {
				t.Fatalf("output = %q, want %q", out.String(), tt.want)
			}
			if tt.dry {
				after, err := os.ReadFile(filepath.Join(stateDir, "state.toml"))
				if err != nil {
					t.Fatal(err)
				}
				if string(before) != string(after) {
					t.Fatal("dry-run modified state")
				}
			}
		})
	}
}

func TestRetryLegacySkippedAndIncompleteState(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cfg := Config{Jobs: []Job{{Name: "job", Scope: "system", Command: "false"}}}
	for _, result := range []string{jobResultFailed, jobResultSkipped, jobResultIncomplete} {
		for _, recent := range []bool{false, true} {
			state := State{LastFinished: now, LastResultByJob: map[string]string{"job": result}}
			if recent {
				state.LastSuccessByJob = map[string]time.Time{"job": now.Add(-time.Hour)}
			}
			due, err := dueJobs(cfg, state, 24*time.Hour, false, now)
			if err != nil || (len(due) == 0) != recent {
				t.Fatalf("legacy %s/recent=%v: due = %v, err = %v", result, recent, due, err)
			}
		}
	}
	stateDir := t.TempDir()
	if _, err := runOnceLocked(cfg, stateDir, "test", false, nil, io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	due, err := dueJobs(cfg, state, 24*time.Hour, false, time.Now())
	if err != nil || len(due) != 1 {
		t.Fatalf("skipped job gained cooldown: due = %v, err = %v", due, err)
	}
}

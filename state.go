package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	incompleteExitCode  = -1
	jobResultSucceeded  = "succeeded"
	jobResultFailed     = "failed"
	jobResultSkipped    = "skipped"
	jobResultIncomplete = "incomplete"
	jobResultNotRun     = "not run"
)

var errAlreadyRunning = errors.New("upkeep is already running")

type State struct {
	LastAttempt      time.Time            `toml:"last_attempt"`
	LastPrompt       time.Time            `toml:"last_prompt"`
	LastFinished     time.Time            `toml:"last_finished"`
	LastSuccess      time.Time            `toml:"last_success"`
	LastSuccessByJob map[string]time.Time `toml:"last_success_by_job"`
	LastFailureByJob map[string]time.Time `toml:"last_failure_by_job"`
	LastResultByJob  map[string]string    `toml:"last_result_by_job"`
	LastExitCode     int                  `toml:"last_exit_code"`
}

func statusCommand(stateDir string, out, errOut io.Writer) int {
	state, err := loadState(stateDir)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	fmt.Fprintln(out, "Last run:")
	fmt.Fprintf(out, "  Started:  %s\n", formatTime(state.LastAttempt))
	fmt.Fprintf(out, "  Finished: %s\n", formatTime(state.LastFinished))
	if runIncomplete(state) {
		fmt.Fprintln(out, "  Result:   incomplete")
	} else if state.LastFinished.IsZero() {
		fmt.Fprintln(out, "  Result:   no run yet")
	} else if state.LastExitCode == 0 {
		fmt.Fprintln(out, "  Result:   succeeded")
	} else {
		fmt.Fprintln(out, "  Result:   failed")
	}
	if len(state.LastResultByJob) > 0 {
		names := make([]string, 0, len(state.LastResultByJob))
		for name := range state.LastResultByJob {
			names = append(names, name)
		}
		sort.Strings(names)
		fmt.Fprintln(out, "  Jobs:")
		for _, name := range names {
			fmt.Fprintf(out, "    %s: %s\n", name, state.LastResultByJob[name])
		}
	}
	fmt.Fprintf(out, "\nLast successful run: %s\n", formatTime(state.LastSuccess))
	return 0
}

func loadState(stateDir string) (State, error) {
	path := filepath.Join(stateDir, "state.toml")
	var state State
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return state, nil
	} else if err != nil {
		return state, err
	}
	if _, err := toml.DecodeFile(path, &state); err != nil {
		return State{}, fmt.Errorf("load state %s: %w", path, err)
	}
	return state, nil
}

func saveState(stateDir string, state State) error {
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return err
	}
	path := filepath.Join(stateDir, "state.toml")
	tmp, err := os.CreateTemp(stateDir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if err := toml.NewEncoder(tmp).Encode(state); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func openLog(stateDir, trigger string, now time.Time) (*os.File, error) {
	logDir := filepath.Join(stateDir, "logs")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return nil, err
	}
	name := now.UTC().Format("20060102T150405.000000000Z") + "-" + trigger + ".log"
	return os.OpenFile(filepath.Join(logDir, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
}

func acquireLock(stateDir string) (func(), error) {
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return nil, err
	}
	lockDir := filepath.Join(stateDir, "lock")
	ownerPath := filepath.Join(lockDir, "owner")
	for {
		if err := os.Mkdir(lockDir, 0700); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return nil, err
			}
			data, readErr := os.ReadFile(ownerPath)
			pid := ownerPID(string(data))
			if readErr != nil || pid <= 0 || pidAlive(pid) {
				return nil, errAlreadyRunning
			}
			if err := os.Remove(ownerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			if err := os.Remove(lockDir); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			continue
		}
		owner := fmt.Sprintf("pid=%d\nstarted_at=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
		if err := os.WriteFile(ownerPath, []byte(owner), 0600); err != nil {
			_ = os.Remove(lockDir)
			return nil, err
		}
		return func() {
			_ = os.Remove(ownerPath)
			_ = os.Remove(lockDir)
		}, nil
	}
}

func unlock(stateDir string) error {
	lockDir := filepath.Join(stateDir, "lock")
	ownerPath := filepath.Join(lockDir, "owner")
	data, err := os.ReadFile(ownerPath)
	if errors.Is(err, os.ErrNotExist) {
		if removeErr := os.Remove(lockDir); errors.Is(removeErr, os.ErrNotExist) {
			return nil
		} else {
			return removeErr
		}
	}
	if err != nil {
		return err
	}
	pid := ownerPID(string(data))
	if pid > 0 && pidAlive(pid) {
		return fmt.Errorf("lock is held by pid %d", pid)
	}
	if err := os.Remove(ownerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Remove(lockDir)
}

func ownerPID(owner string) int {
	for _, line := range strings.Split(owner, "\n") {
		if strings.HasPrefix(line, "pid=") {
			pid, _ := strconv.Atoi(strings.TrimPrefix(line, "pid="))
			return pid
		}
	}
	return 0
}

func pidAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission)
}

func isDue(state State, interval time.Duration, now time.Time) bool {
	return state.LastSuccess.IsZero() || now.Sub(state.LastSuccess) >= interval
}

func (s *State) recordJobSuccess(name string, at time.Time) {
	if s.LastSuccessByJob == nil {
		s.LastSuccessByJob = make(map[string]time.Time)
	}
	s.LastSuccessByJob[name] = at
	delete(s.LastFailureByJob, name)
}

func (s *State) recordJobFailure(name string, at time.Time) {
	if s.LastFailureByJob == nil {
		s.LastFailureByJob = make(map[string]time.Time)
	}
	s.LastFailureByJob[name] = at
}

func withinDuration(t time.Time, duration time.Duration, now time.Time) bool {
	return !t.IsZero() && now.Sub(t) < duration
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "never"
	}
	return value.Local().Format("2006-01-02 15:04:05 MST")
}

func runIncomplete(state State) bool {
	return !state.LastAttempt.IsZero() && (state.LastFinished.IsZero() || state.LastAttempt.After(state.LastFinished))
}

func defaultStateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ".upkeep-state"
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "upkeep")
}

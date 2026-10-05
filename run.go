package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

const (
	ansiReset    = "\x1b[0m"
	ansiCyan     = "\x1b[36m"
	ansiGreen    = "\x1b[32m"
	ansiYellow   = "\x1b[33m"
	ansiRed      = "\x1b[31m"
	jobSeparator = "----------------------------------------"
)

var errApprovalTimeout = errors.New("approval timed out")

type RunResult struct {
	Failed int
}

type runOutput struct {
	log      io.Writer
	terminal io.Writer
	color    bool
}

func (o runOutput) message(color, format string, args ...any) {
	plain := fmt.Sprintf(format, args...)
	if o.log != nil {
		fmt.Fprint(o.log, plain)
	}
	if o.terminal == nil {
		return
	}
	if o.color && color != "" {
		fmt.Fprintf(o.terminal, "%s%s%s", color, plain, ansiReset)
		return
	}
	fmt.Fprint(o.terminal, plain)
}

func (o runOutput) commandWriter() io.Writer {
	if o.terminal == nil {
		return o.log
	}
	return io.MultiWriter(o.log, o.terminal)
}

func (r RunResult) ExitCode() int {
	if r.Failed > 0 {
		return 1
	}
	return 0
}

func runCommand(configPath, stateDir string, names []string, force, dryRun, yes, interactive bool, in io.Reader, out, errOut io.Writer) int {
	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	cfg, err = cfg.selectWorkflows(names)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	snooze, approvalTimeout, err := cfg.durations()
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	if dryRun {
		return dryRunCommand(cfg, stateDir, force, out, errOut)
	}
	if !interactive && !yes {
		return 0
	}
	return runDueCommand(cfg, stateDir, force, snooze, approvalTimeout, yes, interactive, in, out, errOut)
}

func dryRunCommand(cfg Config, stateDir string, force bool, out, errOut io.Writer) int {
	state, err := loadState(stateDir)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	now := time.Now()
	due, err := dueWorkflows(cfg, state, force, now)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	if len(due) > 0 {
		fmt.Fprintln(out, "Updates are due.")
	} else {
		fmt.Fprintln(out, "Updates are not due.")
	}
	fmt.Fprintln(out, "\nDry run:")
	for _, workflow := range cfg.Workflows {
		next, _ := nextWorkflowDue(state, workflow)
		fmt.Fprintf(out, "  %s : ", workflow.Name)
		if force || next.IsZero() || !now.Before(next) {
			fmt.Fprintln(out, "due now")
			for _, job := range workflow.Jobs {
				fmt.Fprintf(out, "    %s (%s)\n", job.Name, job.Scope)
				for _, command := range job.commands() {
					fmt.Fprintf(out, "      %s\n", command)
				}
			}
		} else {
			fmt.Fprintf(out, "next due in %s\n", formatDurationUntil(next.Sub(now)))
		}
	}
	return 0
}

func formatDurationUntil(remaining time.Duration) string {
	if remaining <= 0 {
		return "now"
	}
	units := []struct {
		duration time.Duration
		suffix   string
	}{
		{24 * time.Hour, "d"},
		{time.Hour, "h"},
		{time.Minute, "m"},
		{time.Second, "s"},
	}
	parts := make([]string, 0, 2)
	lastValue := time.Duration(0)
	lastSuffix := ""
	for _, unit := range units {
		if remaining >= unit.duration {
			lastValue = remaining / unit.duration
			lastSuffix = unit.suffix
			parts = append(parts, fmt.Sprintf("%d%s", lastValue, lastSuffix))
			remaining %= unit.duration
			if len(parts) == 2 {
				break
			}
		}
	}
	if len(parts) == 0 {
		return "1s"
	}
	if len(parts) == 1 && remaining > 0 {
		if lastSuffix == "s" {
			parts[0] = fmt.Sprintf("%d%s", lastValue+1, lastSuffix)
		} else {
			parts = append(parts, "1s")
		}
	}
	return strings.Join(parts, " ")
}

func runDueCommand(cfg Config, stateDir string, force bool, snooze, approvalTimeout time.Duration, skipApproval, interactive bool, in io.Reader, out, errOut io.Writer) int {
	state, err := loadState(stateDir)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	due, err := dueWorkflows(cfg, state, force, time.Now())
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	due = unsnoozedWorkflows(due, state, snooze, skipApproval, time.Now())
	if len(due) == 0 {
		return 0
	}
	if !interactive && hasSystemJobs(due) {
		fmt.Fprintln(errOut, "upkeep: system jobs require an interactive terminal")
		return 1
	}
	release, err := acquireLock(stateDir)
	if err != nil {
		if errors.Is(err, errAlreadyRunning) {
			return 0
		}
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	defer release()
	// Re-read after taking the lock so completed runs cannot be overwritten.
	state, err = loadState(stateDir)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	due, err = dueWorkflows(cfg, state, force, time.Now())
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	due = unsnoozedWorkflows(due, state, snooze, skipApproval, time.Now())
	if len(due) == 0 {
		return 0
	}
	if !interactive && hasSystemJobs(due) {
		fmt.Fprintln(errOut, "upkeep: system jobs require an interactive terminal")
		return 1
	}
	if !skipApproval {
		ok, err := askApproval(in, out, approvalTimeout)
		if errors.Is(err, errApprovalTimeout) {
			fmt.Fprintln(errOut, "upkeep: approval timed out; skipping")
			return 0
		}
		if err != nil {
			fmt.Fprintln(errOut, "upkeep:", err)
			return 1
		}
		if !ok {
			now := time.Now()
			for _, workflow := range due {
				state.workflow(workflow.Name).LastPrompt = now
			}
			if err := saveState(stateDir, state); err != nil {
				fmt.Fprintln(errOut, "upkeep:", err)
				return 1
			}
			return 0
		}
	}
	cfg.Workflows = due
	result, err := runOnceLocked(cfg, stateDir, "startup", interactive, in, out)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	return result.ExitCode()
}

func dueWorkflows(cfg Config, state State, force bool, now time.Time) ([]Workflow, error) {
	due := make([]Workflow, 0, len(cfg.Workflows))
	for _, workflow := range cfg.Workflows {
		next, err := nextWorkflowDue(state, workflow)
		if err != nil {
			return nil, err
		}
		if force || next.IsZero() || !now.Before(next) {
			due = append(due, workflow)
		}
	}
	return due, nil
}

func nextWorkflowDue(state State, workflow Workflow) (time.Time, error) {
	interval, err := workflow.duration()
	if err != nil {
		return time.Time{}, err
	}
	history := state.Workflows[workflow.Name]
	if history == nil || history.LastFinished.IsZero() || runIncomplete(*history) {
		return time.Time{}, nil
	}
	return history.LastFinished.Add(interval), nil
}

func unsnoozedWorkflows(workflows []Workflow, state State, snooze time.Duration, yes bool, now time.Time) []Workflow {
	selected := make([]Workflow, 0, len(workflows))
	for _, workflow := range workflows {
		history := state.Workflows[workflow.Name]
		if yes || history == nil || !withinDuration(history.LastPrompt, snooze, now) {
			selected = append(selected, workflow)
		}
	}
	return selected
}

func hasSystemJobs(workflows []Workflow) bool {
	for _, workflow := range workflows {
		for _, job := range workflow.Jobs {
			if job.Scope == "system" {
				return true
			}
		}
	}
	return false
}

func askApproval(in io.Reader, out io.Writer, timeout time.Duration) (bool, error) {
	fmt.Fprint(out, "upkeep: updates are due. Run now? [y/N] ")
	if in == nil {
		return false, errors.New("approval input is unavailable")
	}
	type approvalResult struct {
		ok  bool
		err error
	}
	result := make(chan approvalResult, 1)
	go func() {
		var line strings.Builder
		var one [1]byte
		for {
			n, err := in.Read(one[:])
			if n > 0 {
				if one[0] == '\n' {
					break
				}
				if one[0] != '\r' {
					line.WriteByte(one[0])
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				result <- approvalResult{err: err}
				return
			}
		}
		result <- approvalResult{ok: strings.EqualFold(strings.TrimSpace(line.String()), "y")}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case response := <-result:
		return response.ok, response.err
	case <-timer.C:
		return false, errApprovalTimeout
	}
}

func runOnceLocked(cfg Config, stateDir, trigger string, interactive bool, in io.Reader, out io.Writer) (RunResult, error) {
	state, err := loadState(stateDir)
	if err != nil {
		return RunResult{}, err
	}
	now := time.Now()
	logFile, err := openLog(stateDir, trigger, now)
	if err != nil {
		return RunResult{}, err
	}
	defer logFile.Close()
	output := runOutput{log: logFile, terminal: out, color: isTerminalWriter(out)}
	output.message(ansiCyan, "Started update run at %s.\n", formatTime(now))
	result := RunResult{}
	usePTY := interactive && isTerminalReader(in) && isTerminalWriter(out)
	for _, workflow := range cfg.Workflows {
		history := state.workflow(workflow.Name)
		history.LastAttempt = time.Now()
		history.LastExitCode = incompleteExitCode
		history.LastResultByJob = make(map[string]string, len(workflow.Jobs))
		for _, job := range workflow.Jobs {
			history.LastResultByJob[job.Name] = jobResultNotRun
		}
		if err := saveState(stateDir, state); err != nil {
			return result, err
		}
		failed := 0
		output.message(ansiCyan, "\nWorkflow %q.\n", workflow.Name)
		for _, job := range workflow.Jobs {
			history.LastResultByJob[job.Name] = jobResultIncomplete
			if err := saveState(stateDir, state); err != nil {
				return result, err
			}
			output.message(ansiCyan, "\n%s\nRunning %s job %q.\n%s\n", jobSeparator, job.Scope, job.Name, jobSeparator)
			var jobErr error
			if job.Scope == "system" {
				if !interactive {
					jobErr = errors.New("interactive sudo is required")
				} else {
					jobErr = authenticateSudo(in, out)
				}
				if jobErr != nil {
					output.message(ansiYellow, "Skipped system job %q: sudo authentication failed or unavailable: %v.\n", job.Name, jobErr)
					history.LastResultByJob[job.Name] = jobResultSkipped
				}
			}
			if jobErr == nil {
				commandOutput := output.commandWriter()
				fmt.Fprintln(commandOutput)
				jobErr = executeJob(job, in, commandOutput, usePTY)
				fmt.Fprintln(commandOutput)
				history.LastResultByJob[job.Name] = jobResultSucceeded
				if jobErr != nil {
					history.LastResultByJob[job.Name] = jobResultFailed
				}
			}
			if jobErr != nil {
				output.message(ansiRed, "Job %q failed: %v.\n", job.Name, jobErr)
				failed++
				result.Failed++
			} else {
				output.message(ansiGreen, "Job %q completed.\n%s\n", job.Name, jobSeparator)
			}
			if err := saveState(stateDir, state); err != nil {
				return result, err
			}
		}
		history.LastFinished = time.Now()
		history.LastExitCode = 0
		if failed > 0 {
			history.LastExitCode = 1
		} else {
			history.LastSuccess = history.LastFinished
		}
		if err := saveState(stateDir, state); err != nil {
			return result, err
		}
	}
	if result.Failed == 0 {
		output.message(ansiGreen, "Finished successfully at %s.\n", formatTime(time.Now()))
	} else {
		output.message(ansiRed, "Finished at %s with %d failed job(s).\n", formatTime(time.Now()), result.Failed)
	}
	return result, nil
}

func authenticateSudo(in io.Reader, out io.Writer) error {
	cmd := exec.Command(sudoPath(), "-v")
	cmd.Stdin = in
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

func executeJob(job Job, in io.Reader, output io.Writer, usePTY bool) error {
	for _, command := range job.commands() {
		var cmd *exec.Cmd
		if job.Scope == "system" {
			cmd = exec.Command(sudoPath(), "-n", "/bin/sh", "-c", command)
		} else {
			cmd = exec.Command("/bin/sh", "-c", command)
		}
		if usePTY {
			if err := executeJobWithPTY(cmd, in, output); err != nil {
				return err
			}
			continue
		}
		cmd.Stdin = in
		if cmd.Stdin == nil {
			cmd.Stdin = os.Stdin
		}
		cmd.Stdout = output
		cmd.Stderr = output
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}

func executeJobWithPTY(cmd *exec.Cmd, in io.Reader, output io.Writer) error {
	cmd.Stdin = in
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	ptmx, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{})
	if err != nil {
		cmd.SysProcAttr = nil
		cmd.Stdout = output
		cmd.Stderr = output
		return cmd.Run()
	}
	defer ptmx.Close()
	if inFile, ok := in.(*os.File); ok {
		_ = pty.InheritSize(inFile, ptmx)
	}
	_, _ = io.Copy(output, ptmx)
	return cmd.Wait()
}

var sudoPath = findSudoPath

func findSudoPath() string {
	for _, path := range []string{"/usr/bin/sudo", "/bin/sudo"} {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return "sudo"
}

func isInteractive() bool {
	return isTerminal(os.Stdin) && isTerminal(os.Stdout)
}

func isTerminal(file *os.File) bool {
	return file != nil && term.IsTerminal(int(file.Fd()))
}

func isTerminalReader(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	return ok && isTerminal(file)
}

func isTerminalWriter(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	return ok && isTerminal(file)
}

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

func runCommand(configPath, stateDir, intervalArg string, intervalSet, dryRun, yes, interactive bool, in io.Reader, out, errOut io.Writer) int {
	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}

	interval, snooze, approvalTimeout, err := cfg.durations()
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	if intervalSet {
		interval, err = parseRunInterval(intervalArg)
		if err != nil {
			fmt.Fprintln(errOut, "upkeep:", err)
			return 1
		}
	}
	if dryRun {
		return dryRunCommand(cfg, stateDir, interval, intervalSet, out, errOut)
	}
	if !interactive && !yes {
		return 0
	}
	return runDueCommand(cfg, stateDir, interval, intervalSet, snooze, approvalTimeout, yes, interactive, in, out, errOut)
}

func dryRunCommand(cfg Config, stateDir string, interval time.Duration, intervalOverride bool, out, errOut io.Writer) int {
	state, err := loadState(stateDir)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	now := time.Now()
	due, err := dueJobs(cfg, state, interval, intervalOverride, now)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	dueSet := make(map[string]bool, len(due))
	for _, job := range due {
		dueSet[job.Name] = true
	}

	if len(due) > 0 {
		fmt.Fprintln(out, "Updates are due.")
	} else {
		fmt.Fprintln(out, "Updates are not due.")
	}
	fmt.Fprintln(out, "\nDry run:")
	first := true
	for _, job := range cfg.Jobs {
		if !first {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "  %s (%s) : ", job.Name, job.Scope)
		if dueSet[job.Name] {
			fmt.Fprintln(out, "due now")
			fmt.Fprintf(out, "    %s\n", job.Command)
		} else {
			jobInterval, err := effectiveJobInterval(job, interval, intervalOverride)
			if err != nil {
				fmt.Fprintln(errOut, "upkeep:", err)
				return 1
			}
			nextDue := state.LastSuccessByJob[job.Name].Add(jobInterval)
			fmt.Fprintf(out, "next due in %s\n", formatDurationUntil(nextDue.Sub(now)))
		}
		first = false
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

func runDueCommand(cfg Config, stateDir string, interval time.Duration, intervalOverride bool, snooze, approvalTimeout time.Duration, skipApproval, interactive bool, in io.Reader, out, errOut io.Writer) int {
	state, err := loadState(stateDir)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	now := time.Now()
	due, err := dueJobs(cfg, state, interval, intervalOverride, now)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	if len(due) == 0 || (!skipApproval && withinDuration(state.LastPrompt, snooze, now)) {
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

	// Another terminal may have completed the run while this process waited
	// for the lock. Re-read state before prompting.
	state, err = loadState(stateDir)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	now = time.Now()
	due, err = dueJobs(cfg, state, interval, intervalOverride, now)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	if len(due) == 0 || (!skipApproval && withinDuration(state.LastPrompt, snooze, now)) {
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
			state.LastPrompt = now
			if err := saveState(stateDir, state); err != nil {
				fmt.Fprintln(errOut, "upkeep:", err)
				return 1
			}
			return 0
		}
	}

	dueSet := make(map[string]bool, len(due))
	for _, job := range due {
		dueSet[job.Name] = true
	}
	result, err := runOnceLocked(cfg, stateDir, "startup", interactive, in, out, dueSet)
	if err != nil {
		fmt.Fprintln(errOut, "upkeep:", err)
		return 1
	}
	return result.ExitCode()
}

func dueJobs(cfg Config, state State, globalInterval time.Duration, intervalOverride bool, now time.Time) ([]Job, error) {
	due := make([]Job, 0, len(cfg.Jobs))
	for _, job := range cfg.Jobs {
		interval, err := effectiveJobInterval(job, globalInterval, intervalOverride)
		if err != nil {
			return nil, err
		}
		if isJobDue(state, job.Name, interval, now) {
			due = append(due, job)
		}
	}
	return due, nil
}

func hasSystemJobs(jobs []Job) bool {
	for _, job := range jobs {
		if job.Scope == "system" {
			return true
		}
	}
	return false
}

func parseRunInterval(value string) (time.Duration, error) {
	interval, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid run interval %q: %w", value, err)
	}
	if interval < 0 {
		return 0, fmt.Errorf("run interval must not be negative")
	}
	return interval, nil
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

func runOnceLocked(cfg Config, stateDir, trigger string, interactive bool, in io.Reader, out io.Writer, due map[string]bool) (RunResult, error) {
	state, err := loadState(stateDir)
	if err != nil {
		return RunResult{}, err
	}
	now := time.Now()
	state.LastAttempt = now
	state.LastExitCode = incompleteExitCode
	state.LastResultByJob = make(map[string]string, len(cfg.Jobs))
	for _, job := range cfg.Jobs {
		state.LastResultByJob[job.Name] = jobResultNotRun
		if due == nil || due[job.Name] {
			state.LastResultByJob[job.Name] = jobResultIncomplete
		}
	}
	if err := saveState(stateDir, state); err != nil {
		return RunResult{}, err
	}
	checkpointJob := func(name, jobResult string, success bool) error {
		state.LastResultByJob[name] = jobResult
		if success {
			state.recordJobSuccess(name, time.Now())
		}
		return saveState(stateDir, state)
	}

	logFile, err := openLog(stateDir, trigger, now)
	if err != nil {
		return RunResult{}, err
	}
	defer logFile.Close()

	output := runOutput{
		log:      logFile,
		terminal: out,
		color:    isTerminalWriter(out),
	}
	output.message(ansiCyan, "Started update run at %s.\n", formatTime(now))

	result := RunResult{}
	usePTY := interactive && isTerminalReader(in) && isTerminalWriter(out)
	for _, job := range cfg.Jobs {
		if due != nil && !due[job.Name] {
			continue
		}
		output.message(ansiCyan, "\n%s\nRunning %s job %q.\n%s\n", jobSeparator, job.Scope, job.Name, jobSeparator)

		if job.Scope == "system" {
			if !interactive {
				output.message(ansiYellow, "Skipped system job %q: interactive sudo is required.\n", job.Name)
				result.Failed++
				if err := checkpointJob(job.Name, jobResultSkipped, false); err != nil {
					return result, err
				}
				continue
			}
			if err := authenticateSudo(in, out); err != nil {
				output.message(ansiRed, "Sudo authentication failed: %v.\n", err)
				output.message(ansiYellow, "Skipped system job %q: sudo authentication failed.\n", job.Name)
				result.Failed++
				if err := checkpointJob(job.Name, jobResultSkipped, false); err != nil {
					return result, err
				}
				continue
			}
		}

		commandOutput := output.commandWriter()
		fmt.Fprintln(commandOutput)
		err := executeJob(job, in, commandOutput, usePTY)
		fmt.Fprintln(commandOutput)
		if err != nil {
			output.message(ansiRed, "Job %q failed: %v.\n", job.Name, err)
			result.Failed++
			if err := checkpointJob(job.Name, jobResultFailed, false); err != nil {
				return result, err
			}
			continue
		}
		if err := checkpointJob(job.Name, jobResultSucceeded, true); err != nil {
			return result, err
		}
		output.message(ansiGreen, "Job %q completed.\n%s\n", job.Name, jobSeparator)
	}

	finished := time.Now()
	state.LastFinished = finished
	state.LastExitCode = result.ExitCode()
	if result.Failed == 0 {
		state.LastSuccess = finished
	}
	if err := saveState(stateDir, state); err != nil {
		return result, err
	}
	if result.Failed == 0 {
		output.message(ansiGreen, "Finished successfully at %s.\n", formatTime(finished))
	} else {
		output.message(ansiRed, "Finished at %s with %d failed job(s).\n", formatTime(finished), result.Failed)
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
	var cmd *exec.Cmd
	if job.Scope == "system" {
		cmd = exec.Command(sudoPath(), "-n", "/bin/sh", "-c", job.Command)
	} else {
		cmd = exec.Command("/bin/sh", "-c", job.Command)
	}
	if usePTY {
		return executeJobWithPTY(cmd, in, output)
	}
	cmd.Stdin = in
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	cmd.Stdout = output
	cmd.Stderr = output
	return cmd.Run()
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

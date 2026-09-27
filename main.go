package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// version is replaced by release builds with -ldflags. Local source builds
// intentionally report dev when no build metadata is supplied.
var version = "dev"

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func runCLI(args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) == 0 {
		printUsage(out)
		return 2
	}

	command := args[0]
	args = args[1:]
	if command == "-v" || command == "--version" {
		printVersion(out)
		return 0
	}
	if command == "-h" || command == "--help" {
		printUsage(out)
		return 0
	}
	if hasHelpFlag(args) {
		switch command {
		case "run", "config", "status", "unlock":
			printCommandUsage(out, command)
			return 0
		}
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() { printCommandUsage(errOut, command) }
	configPath := defaultConfigPath()
	stateDir := defaultStateDir()
	if command == "run" || command == "config" {
		fs.StringVar(&configPath, "config", configPath, "path to config.toml")
	}
	fs.StringVar(&stateDir, "state-dir", stateDir, "directory for state, locks, and logs")
	intervalArg := ""
	dryRun := false
	yes := false
	if command == "run" {
		fs.StringVar(&intervalArg, "interval", "", "override all job intervals for this run")
		fs.BoolVar(&dryRun, "dry-run", false, "show due jobs without running them")
		fs.BoolVar(&yes, "yes", false, "run without asking for approval")
		fs.BoolVar(&yes, "y", false, "run without asking for approval")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(errOut, "upkeep: unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}
	intervalSet := false
	fs.Visit(func(f *flag.Flag) {
		intervalSet = intervalSet || f.Name == "interval"
	})

	switch command {
	case "run":
		return runCommand(configPath, stateDir, intervalArg, intervalSet, dryRun, yes, isInteractive(), in, out, errOut)
	case "config":
		return configCommand(configPath, stateDir, out, errOut)
	case "unlock":
		if err := unlock(stateDir); err != nil {
			fmt.Fprintln(errOut, "upkeep:", err)
			return 1
		}
		return 0
	case "status":
		return statusCommand(stateDir, out, errOut)
	case "-h", "--help":
		printUsage(out)
		return 0
	default:
		fmt.Fprintf(errOut, "upkeep: unknown command %q\n", command)
		printUsage(errOut)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: upkeep <command> [options]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  run       Run due jobs.")
	fmt.Fprintln(w, "  config    Show configuration and state paths.")
	fmt.Fprintln(w, "  status    Show the last run status.")
	fmt.Fprintln(w, "  unlock    Remove a stale run lock.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Options:")
	fmt.Fprintln(w, "  -v, --version  Show the upkeep version.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run `upkeep <command> --help` for command options.")
}

func printCommandUsage(w io.Writer, command string) {
	usage := ""
	description := ""
	options := []string{}
	switch command {
	case "run":
		usage = "Usage: upkeep run [options]"
		description = "Run due jobs."
		options = []string{
			"  --interval DURATION  override all job intervals for this run.",
			"  --dry-run            show due jobs without running them.",
			"  --yes, -y            run without asking for approval.",
			"  --config PATH        path to config.toml.",
			"  --state-dir PATH     directory for state, locks, and logs.",
		}
	case "config":
		usage = "Usage: upkeep config [options]"
		description = "Show configuration and state paths."
		options = []string{
			"  --config PATH        path to config.toml.",
			"  --state-dir PATH     directory for state, locks, and logs.",
		}
	case "status":
		usage = "Usage: upkeep status [options]"
		description = "Show the last run times and result."
		options = []string{
			"  --state-dir PATH     directory for state, locks, and logs.",
		}
	case "unlock":
		usage = "Usage: upkeep unlock [options]"
		description = "Remove a lock left by a process that is no longer running."
		options = []string{
			"  --state-dir PATH     directory for state, locks, and logs.",
		}
	default:
		printUsage(w)
		return
	}
	fmt.Fprintf(w, "%s\n\n%s\n\nOptions:\n", usage, description)
	for _, option := range options {
		fmt.Fprintln(w, option)
	}
}

func printVersion(w io.Writer) {
	fmt.Fprintf(w, "upkeep %s\n", version)
}

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

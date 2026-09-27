package main

import (
	"strings"
	"testing"
)

func TestRunCLIHelpListsCommands(t *testing.T) {
	var output, errorsOutput strings.Builder
	code := runCLI([]string{"--help"}, strings.NewReader(""), &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q", code, errorsOutput.String())
	}
	for _, want := range []string{
		"Usage: upkeep <command> [options]",
		"run       Run due jobs.",
		"config    Show configuration and state paths.",
		"status    Show the last run status.",
		"unlock    Remove a stale run lock.",
		"-v, --version  Show the upkeep version.",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("help = %q, want %q", output.String(), want)
		}
	}
	if strings.Contains(output.String(), "--interval overrides all job intervals") {
		t.Fatalf("help = %q, want a generic run summary", output.String())
	}
	if strings.Contains(output.String(), "  help ") {
		t.Fatalf("help = %q, want no help subcommand", output.String())
	}
	if strings.Contains(output.String(), "check") {
		t.Fatalf("help = %q, want no check command", output.String())
	}
}

func TestRunCLIVersion(t *testing.T) {
	for _, arg := range []string{"--version", "-v"} {
		var output, errorsOutput strings.Builder
		code := runCLI([]string{arg}, strings.NewReader(""), &output, &errorsOutput)
		if code != 0 {
			t.Fatalf("%s exit code = %d, errors = %q", arg, code, errorsOutput.String())
		}
		if output.String() != "upkeep dev\n" {
			t.Fatalf("%s output = %q, want version", arg, output.String())
		}
	}
}

func TestRunCLICommandHelpDescribesRun(t *testing.T) {
	var output, errorsOutput strings.Builder
	code := runCLI([]string{"run", "--help"}, strings.NewReader(""), &output, &errorsOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, errors = %q", code, errorsOutput.String())
	}
	for _, want := range []string{
		"Usage: upkeep run [options]",
		"Run due jobs.",
		"Options:",
		"  --interval DURATION  override all job intervals for this run.",
		"  --dry-run            show due jobs without running them.",
		"  --yes, -y            run without asking for approval.",
		"  --config PATH        path to config.toml.",
		"  --state-dir PATH     directory for state, locks, and logs.",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("command help = %q, want %q", output.String(), want)
		}
	}
}

func TestRunCLICommandHelpUsesStandardStructure(t *testing.T) {
	tests := []struct {
		command     string
		usage       string
		description string
		options     []string
	}{
		{
			command:     "config",
			usage:       "Usage: upkeep config [options]",
			description: "Show configuration and state paths.",
			options: []string{
				"  --config PATH        path to config.toml.",
				"  --state-dir PATH     directory for state, locks, and logs.",
			},
		},
		{
			command:     "status",
			usage:       "Usage: upkeep status [options]",
			description: "Show the last run times and result.",
			options: []string{
				"  --state-dir PATH     directory for state, locks, and logs.",
			},
		},
		{
			command:     "unlock",
			usage:       "Usage: upkeep unlock [options]",
			description: "Remove a lock left by a process that is no longer running.",
			options: []string{
				"  --state-dir PATH     directory for state, locks, and logs.",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			var output, errorsOutput strings.Builder
			code := runCLI([]string{test.command, "--help"}, strings.NewReader(""), &output, &errorsOutput)
			if code != 0 {
				t.Fatalf("exit code = %d, errors = %q", code, errorsOutput.String())
			}
			for _, want := range append([]string{test.usage, test.description, "Options:"}, test.options...) {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("help = %q, want %q", output.String(), want)
				}
			}
		})
	}
}

func TestRunCLIRejectsCheckCommand(t *testing.T) {
	var output, errorsOutput strings.Builder
	code := runCLI([]string{"check"}, strings.NewReader(""), &output, &errorsOutput)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errorsOutput.String(), `unknown command "check"`) {
		t.Fatalf("errors = %q, want an unknown command error", errorsOutput.String())
	}
}

func TestRunCLIRejectsHelpCommand(t *testing.T) {
	var output, errorsOutput strings.Builder
	code := runCLI([]string{"help"}, strings.NewReader(""), &output, &errorsOutput)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errorsOutput.String(), `unknown command "help"`) {
		t.Fatalf("errors = %q, want an unknown command error", errorsOutput.String())
	}
}

func TestRunCLIRejectsUnexpectedArguments(t *testing.T) {
	var output, errorsOutput strings.Builder
	code := runCLI([]string{"status", "typo"}, strings.NewReader(""), &output, &errorsOutput)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errorsOutput.String(), "unexpected arguments: typo") {
		t.Fatalf("errors = %q, want unexpected argument error", errorsOutput.String())
	}
}

func TestRunCLIRejectsUnusedConfigFlag(t *testing.T) {
	for _, command := range []string{"status", "unlock"} {
		var output, errorsOutput strings.Builder
		code := runCLI([]string{command, "--config", "ignored.toml"}, strings.NewReader(""), &output, &errorsOutput)
		if code != 2 {
			t.Fatalf("%s exit code = %d, want 2", command, code)
		}
		if !strings.Contains(errorsOutput.String(), "flag provided but not defined: -config") {
			t.Fatalf("%s errors = %q, want undefined flag error", command, errorsOutput.String())
		}
	}
}

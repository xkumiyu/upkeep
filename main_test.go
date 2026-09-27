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
		"run       Run due jobs; --interval overrides all job intervals.",
		"config    Show configuration and state paths.",
		"status    Show the last run status.",
		"unlock    Remove a stale run lock.",
		"-v, --version  Show the upkeep version.",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("help = %q, want %q", output.String(), want)
		}
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
		"Usage: upkeep run [--interval DURATION] [--dry-run] [--yes] [--config PATH] [--state-dir PATH]",
		"Preview due jobs with --dry-run; otherwise run them after asking for approval. --interval overrides every job interval; use -y or --yes to skip approval.",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("command help = %q, want %q", output.String(), want)
		}
	}
}

func TestRunCLICommandHelpDescribesAllCommands(t *testing.T) {
	for _, command := range []string{"config", "status", "unlock"} {
		var output, errorsOutput strings.Builder
		code := runCLI([]string{command, "--help"}, strings.NewReader(""), &output, &errorsOutput)
		if code != 0 {
			t.Fatalf("%s help exit code = %d, errors = %q", command, code, errorsOutput.String())
		}
		if !strings.Contains(output.String(), "Usage: upkeep "+command) {
			t.Fatalf("%s help = %q, want command usage", command, output.String())
		}
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

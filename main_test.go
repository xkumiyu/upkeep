package main

import (
	"io"
	"os"
	"path/filepath"
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
		"run       Run due workflows.",
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
		"Usage: upkeep run [workflow...] [options]",
		"Run selected workflows in declaration order; no names selects all workflows.",
		"Options:",
		"  --force              ignore deadlines; approval is still required.",
		"  --dry-run            show workflow deadlines and due jobs without running them.",
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

func TestWorkflowConfigAndInterspersedSelection(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	err := os.WriteFile(path, []byte(`[[workflows]]
name="dev-tools"
interval="168h"
[[workflows.jobs]]
name="mise"
command="printf done"
`), 0600)
	if err != nil {
		t.Fatal(err)
	}
	var out, errs strings.Builder
	if code := runCLI([]string{"run", "dev-tools", "--config", path, "--dry-run", "dev-tools", "--state-dir", filepath.Join(root, "state")}, nil, &out, &errs); code != 0 {
		t.Fatalf("code=%d errors=%s", code, errs.String())
	}
	if strings.Count(out.String(), "printf done") != 1 {
		t.Fatalf("output=%s", out.String())
	}
}

func TestWorkflowCLISelectionOrderAndTerminator(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	config := `[[workflows]]
name="first"
interval="24h"
[[workflows.jobs]]
name="shared"
command="printf FIRST"
[[workflows]]
name="second"
interval="24h"
[[workflows.jobs]]
name="shared"
command="printf SECOND"
[[workflows]]
name="--force"
interval="24h"
[[workflows.jobs]]
name="shared"
command="printf DASH"
`
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errs strings.Builder
	dir := filepath.Join(root, "state")
	if code := runCLI([]string{"run", "second", "--config", path, "-y", "first", "--state-dir", dir, "second"}, nil, &out, &errs); code != 0 {
		t.Fatal(errs.String())
	}
	if strings.Count(out.String(), "FIRST") != 1 || strings.Count(out.String(), "SECOND") != 1 || strings.Index(out.String(), "FIRST") > strings.Index(out.String(), "SECOND") || strings.Contains(out.String(), "DASH") {
		t.Fatal(out.String())
	}
	out.Reset()
	// --force after -- is a literal workflow name, even when flags precede --.
	if code := runCLI([]string{"run", "--yes", "--config", path, "--state-dir", dir, "--", "first", "--force"}, nil, &out, &errs); code != 0 {
		t.Fatal(errs.String())
	}
	if strings.Contains(out.String(), "FIRST") || !strings.Contains(out.String(), "DASH") {
		t.Fatalf("terminator ignored: %s", out.String())
	}
	out.Reset()
	if code := runCLI([]string{"run", "--config", path, "--state-dir", filepath.Join(root, "absent"), "--yes", "first", "missing"}, nil, &out, &errs); code != 1 || !strings.Contains(errs.String(), `unknown workflow "missing"`) {
		t.Fatalf("out=%s errors=%s", out.String(), errs.String())
	}
	if _, err := os.Stat(filepath.Join(root, "absent")); !os.IsNotExist(err) {
		t.Fatal("unknown selection caused side effects")
	}
	for _, flag := range []string{"--interval=0", "--group=first"} {
		if code := runCLI([]string{"run", flag}, nil, io.Discard, io.Discard); code != 2 {
			t.Fatalf("legacy flag %s accepted", flag)
		}
	}
}

func TestWorkflowCLIFlagValuesAndLiteralHelp(t *testing.T) {
	var out, errs strings.Builder
	if code := runCLI([]string{"run", "--config", "--help", "--dry-run"}, nil, &out, &errs); code != 1 || !strings.Contains(errs.String(), "--help") {
		t.Fatalf("dash value treated as flag: code=%d out=%s errors=%s", code, out.String(), errs.String())
	}
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	if err := os.WriteFile(path, []byte("[[workflows]]\nname=\"dev-tools\"\ninterval=\"24h\"\n[[workflows.jobs]]\nname=\"job\"\ncommand=\"true\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errs.Reset()
	if code := runCLI([]string{"run", "--config", path, "--yes", "--", "--help"}, nil, &out, &errs); code != 1 || !strings.Contains(errs.String(), `unknown workflow "--help"`) {
		t.Fatalf("literal help parsed as help: %s %s", out.String(), errs.String())
	}
}

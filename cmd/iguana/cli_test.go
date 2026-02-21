package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"iguana/internal/container"
)

// withTempHome sets HOME to a temp dir for the duration of the test.
func withTempHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	return tmp
}

// ---------------------------------------------------------------------------
// Help / dispatch infrastructure
// ---------------------------------------------------------------------------

func TestCommandsSliceNotEmpty(t *testing.T) {
	if len(commands) == 0 {
		t.Fatal("commands slice is empty — no subcommands registered")
	}
}

func TestCommandsHaveRequiredFields(t *testing.T) {
	for _, cmd := range commands {
		if cmd.name == "" {
			t.Error("command with empty name found")
		}
		if cmd.short == "" {
			t.Errorf("command %q has empty short description", cmd.name)
		}
		if cmd.usage == "" {
			t.Errorf("command %q has empty usage line", cmd.name)
		}
		if cmd.run == nil {
			t.Errorf("command %q has nil run func", cmd.name)
		}
	}
}

func TestHelpContainsAllCommands(t *testing.T) {
	var sb strings.Builder
	printUsage(&sb)
	help := sb.String()
	for _, cmd := range commands {
		if !strings.Contains(help, cmd.name) {
			t.Errorf("help output missing command %q", cmd.name)
		}
	}
}

func TestDispatchNoArgs(t *testing.T) {
	if err := dispatch([]string{}); err != nil {
		t.Fatalf("dispatch with no args should not error: %v", err)
	}
}

func TestDispatchHelpFlag(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		if err := dispatch([]string{flag}); err != nil {
			t.Fatalf("dispatch(%q) should not error: %v", flag, err)
		}
	}
}

func TestDispatchHelpCmd(t *testing.T) {
	if err := dispatch([]string{"help"}); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range commands {
		if err := dispatch([]string{"help", cmd.name}); err != nil {
			t.Fatalf("help %s: %v", cmd.name, err)
		}
	}
	// Unknown command name in help — not an error.
	if err := dispatch([]string{"help", "unknowncmd"}); err != nil {
		t.Fatalf("help unknowncmd should not error: %v", err)
	}
}

func TestDispatchUnknownCommand(t *testing.T) {
	if err := dispatch([]string{"notacommand-xyz"}); err == nil {
		t.Fatal("expected error for unknown command")
	}
}

// ---------------------------------------------------------------------------
// init command
// ---------------------------------------------------------------------------

func TestRunInitCreatesContainer(t *testing.T) {
	tmp := withTempHome(t)
	if err := dispatch([]string{"init", "mycontainer"}); err != nil {
		t.Fatalf("init: %v", err)
	}
	dir := filepath.Join(tmp, ".iguana", "mycontainer")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("container dir not created: %v", err)
	}
}

func TestRunInitDuplicateFails(t *testing.T) {
	withTempHome(t)
	if err := dispatch([]string{"init", "dup"}); err != nil {
		t.Fatalf("first init: %v", err)
	}
	if err := dispatch([]string{"init", "dup"}); err == nil {
		t.Fatal("expected error on duplicate init")
	}
}

func TestRunInitMissingArgFails(t *testing.T) {
	if err := dispatch([]string{"init"}); err == nil {
		t.Fatal("expected error for missing name arg")
	}
}

// ---------------------------------------------------------------------------
// add command
// ---------------------------------------------------------------------------

func TestRunAddMissingArgsFails(t *testing.T) {
	if err := dispatch([]string{"add"}); err == nil {
		t.Fatal("expected error for missing args")
	}
	if err := dispatch([]string{"add", "only-one"}); err == nil {
		t.Fatal("expected error for missing project arg")
	}
}

func TestRunAddMissingContainerFails(t *testing.T) {
	withTempHome(t)
	if err := runAdd([]string{"nocontainer", "proj"}); err == nil {
		t.Fatal("expected error for missing container")
	}
}

// ---------------------------------------------------------------------------
// analyze command
// ---------------------------------------------------------------------------

func TestRunAnalyzeMissingArgFails(t *testing.T) {
	if err := dispatch([]string{"analyze"}); err == nil {
		t.Fatal("expected error for missing container arg")
	}
}

func TestRunAnalyzeEmptyContainer(t *testing.T) {
	withTempHome(t)
	if err := container.Init("emptyc"); err != nil {
		t.Fatal(err)
	}
	if err := dispatch([]string{"analyze", "emptyc"}); err != nil {
		t.Fatalf("analyze empty container: %v", err)
	}
}

func TestRunAnalyzeMissingContainerFails(t *testing.T) {
	withTempHome(t)
	if err := dispatch([]string{"analyze", "nosuchcontainer"}); err == nil {
		t.Fatal("expected error for missing container")
	}
}

// ---------------------------------------------------------------------------
// plugin registry
// ---------------------------------------------------------------------------

func TestPluginRegistryContainsStatic(t *testing.T) {
	p, ok := plugins["static"]
	if !ok {
		t.Fatal("plugin registry missing 'static'")
	}
	if p.Name() != "static" {
		t.Errorf("unexpected name: %q", p.Name())
	}
}

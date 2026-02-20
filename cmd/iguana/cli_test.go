package main

import (
	"strings"
	"testing"
)

// CLI Dispatch Invariants (from INVARIANT.md §CLI Dispatch Invariants)
//
// 32. Known subcommand dispatch: matching name → run(remainingArgs)
// 33. Help flags: iguana / --help / -h → same usage listing; help <cmd> → long help
// 34. Unknown subcommand error: non-existent name → error with suggestion
// 35. Backward compat: existing file/dir → old behavior
// 36. Per-command usage on bad args: wrong args → usage line + non-zero exit
// 37. No-args exits 0: iguana with no args prints help, exits 0
// 38. Commands slice is single source of truth for dispatch and help

// helpText calls the help function and returns the output as a string.
func helpText() string {
	var sb strings.Builder
	printUsage(&sb)
	return sb.String()
}

// longHelpText returns the long help for a named command.
func longHelpText(name string) string {
	var sb strings.Builder
	printCommandHelp(&sb, name)
	return sb.String()
}

// TestHelpContainsAllCommands verifies invariant 38:
// The help listing is derived from the commands slice — every registered
// command name appears in the overall help output.
func TestHelpContainsAllCommands(t *testing.T) {
	help := helpText()
	for _, cmd := range commands {
		if !strings.Contains(help, cmd.name) {
			t.Errorf("help output missing command %q", cmd.name)
		}
		if !strings.Contains(help, cmd.short) {
			t.Errorf("help output missing short description for %q", cmd.short)
		}
	}
}

// TestHelpContainsUsageHeader verifies the overall help has a usage header.
func TestHelpContainsUsageHeader(t *testing.T) {
	help := helpText()
	if !strings.Contains(help, "Usage:") {
		t.Error("help output missing 'Usage:' header")
	}
	if !strings.Contains(help, "iguana") {
		t.Error("help output missing program name 'iguana'")
	}
}

// TestLongHelpForKnownCommands verifies that each registered command has
// a long help section containing its usage line (invariant 33).
func TestLongHelpForKnownCommands(t *testing.T) {
	for _, cmd := range commands {
		t.Run(cmd.name, func(t *testing.T) {
			out := longHelpText(cmd.name)
			if out == "" {
				t.Fatalf("printCommandHelp(%q) returned empty output", cmd.name)
			}
			if !strings.Contains(out, cmd.usage) {
				t.Errorf("long help for %q missing usage line %q\ngot: %s", cmd.name, cmd.usage, out)
			}
		})
	}
}

// TestLongHelpUnknownCommand verifies that help for an unknown command name
// prints an error / fallback (invariant 33).
func TestLongHelpUnknownCommand(t *testing.T) {
	out := longHelpText("no-such-command")
	if !strings.Contains(out, "unknown") && !strings.Contains(out, "no-such-command") {
		t.Errorf("expected unknown-command message, got: %s", out)
	}
}

// TestDispatchKnownSubcommand verifies invariant 32:
// dispatch() routes known command names to their run func and passes
// the remaining args unchanged.
func TestDispatchKnownSubcommand(t *testing.T) {
	// We use the "analyze" command with deliberately wrong args so its
	// run func returns an error — that confirms dispatch reached it.
	// (The command validates its own args; wrong args → usage error.)
	err := dispatch([]string{"analyze"})
	// Should get a usage/arg error from the subcommand, not "unknown command".
	if err == nil {
		t.Fatal("expected error for analyze with no dir, got nil")
	}
	if strings.Contains(err.Error(), "unknown command") {
		t.Errorf("got 'unknown command' error for known subcommand 'analyze': %v", err)
	}
}

// TestDispatchHelpFlag verifies invariant 33: --help / -h produce help (no error).
func TestDispatchHelpFlag(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		t.Run(flag, func(t *testing.T) {
			err := dispatch([]string{flag})
			if err != nil {
				t.Errorf("dispatch(%q) returned error: %v", flag, err)
			}
		})
	}
}

// TestDispatchNoArgs verifies invariant 37: no args → help (no error, exit 0).
func TestDispatchNoArgs(t *testing.T) {
	err := dispatch([]string{})
	if err != nil {
		t.Errorf("dispatch() with no args returned error: %v", err)
	}
}

// TestDispatchHelpSubcommand verifies "help <cmd>" works (invariant 33).
func TestDispatchHelpSubcommand(t *testing.T) {
	for _, cmd := range commands {
		t.Run(cmd.name, func(t *testing.T) {
			err := dispatch([]string{"help", cmd.name})
			if err != nil {
				t.Errorf("dispatch(help %q) returned error: %v", cmd.name, err)
			}
		})
	}
}

// TestDispatchUnknownNonExistentArg verifies invariant 34:
// An unknown first arg that is NOT an existing file returns an error
// suggesting iguana help.
func TestDispatchUnknownNonExistentArg(t *testing.T) {
	err := dispatch([]string{"no-such-command-xyz-abc"})
	if err == nil {
		t.Fatal("expected error for unknown non-existent arg, got nil")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "unknown") {
		t.Errorf("expected 'unknown' in error, got: %s", errMsg)
	}
}

// TestSubcommandBadArgsGivesUsage verifies invariant 36:
// Each subcommand with wrong args returns an error (not a panic).
func TestSubcommandBadArgsGivesUsage(t *testing.T) {
	// Commands that require args: system-model, obsidian-vault both need a dir.
	// analyze needs a dir/file. clean has an optional arg so it won't fail.
	requireArgs := []string{"system-model", "obsidian-vault", "analyze"}
	for _, name := range requireArgs {
		t.Run(name, func(t *testing.T) {
			err := dispatch([]string{name}) // no args after subcommand name
			if err == nil {
				t.Errorf("dispatch(%q) with no args should return error", name)
			}
			// Must not be an "unknown command" error — must have reached the subcommand.
			if strings.Contains(err.Error(), "unknown command") {
				t.Errorf("dispatch(%q) gave 'unknown command', expected subcommand usage error", name)
			}
		})
	}
}

// TestCommandsSliceNotEmpty ensures the commands slice is populated (invariant 38).
func TestCommandsSliceNotEmpty(t *testing.T) {
	if len(commands) == 0 {
		t.Fatal("commands slice is empty — no subcommands registered")
	}
}

// TestCommandsHaveRequiredFields verifies every command has name, short, usage set.
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

package main

// settings_test.go — Tests for settings loading and deny-pattern matching.

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// parseDenyRule
// ---------------------------------------------------------------------------

func TestParseDenyRule(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Read() wrapper stripped, leading ./ stripped.
		{"Read(./baml_client/**)", "baml_client/**"},
		// Leading ./ stripped without Read wrapper.
		{"./baml_client/**", "baml_client/**"},
		// Bare pattern unchanged.
		{"baml_client/**", "baml_client/**"},
		// Read() with no leading ./.
		{"Read(vendor/**)", "vendor/**"},
	}
	for _, tc := range tests {
		got := parseDenyRule(tc.input)
		if got != tc.want {
			t.Errorf("parseDenyRule(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// matchDenyPattern
// ---------------------------------------------------------------------------

func TestMatchDenyPattern(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		// /** matches the prefix dir itself.
		{"baml_client/**", "baml_client", true},
		// /** matches files directly inside.
		{"baml_client/**", "baml_client/async.go", true},
		// /** matches files in subdirectories.
		{"baml_client/**", "baml_client/types/foo.go", true},
		// /** does not match sibling paths.
		{"baml_client/**", "other/baml_client/foo.go", false},
		// /** does not match unrelated paths.
		{"baml_client/**", "main.go", false},
		// Single * matches within one path segment.
		{"*.go", "main.go", true},
		{"*.go", "dir/main.go", false},
		// Exact match.
		{"vendor", "vendor", true},
		{"vendor", "vendor/foo.go", false},
	}
	for _, tc := range tests {
		got := matchDenyPattern(tc.pattern, tc.path)
		if got != tc.want {
			t.Errorf("matchDenyPattern(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// IsDenied
// ---------------------------------------------------------------------------

func TestSettings_IsDenied(t *testing.T) {
	s := &Settings{
		Permissions: Permissions{
			Deny: []string{
				"Read(./baml_client/**)",
				"vendor/**",
			},
		},
	}

	denied := []string{
		"baml_client",
		"baml_client/async.go",
		"baml_client/types/foo.go",
		"vendor",
		"vendor/gopkg.in/foo.go",
	}
	allowed := []string{
		"main.go",
		"system_model.go",
		"other/baml_client/foo.go",
	}

	for _, p := range denied {
		if !s.IsDenied(p) {
			t.Errorf("IsDenied(%q) = false, want true", p)
		}
	}
	for _, p := range allowed {
		if s.IsDenied(p) {
			t.Errorf("IsDenied(%q) = true, want false", p)
		}
	}
}

func TestSettings_IsDenied_NilReceiver(t *testing.T) {
	var s *Settings
	if s.IsDenied("anything") {
		t.Error("nil Settings.IsDenied should always return false")
	}
}

// ---------------------------------------------------------------------------
// LoadSettings
// ---------------------------------------------------------------------------

func TestLoadSettings_FileNotExist(t *testing.T) {
	dir := t.TempDir()
	s, err := LoadSettings(dir)
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if s != nil {
		t.Fatalf("expected nil settings for missing file, got: %+v", s)
	}
}

func TestLoadSettings_ValidFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".iguana"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `
permissions:
  deny:
    - "Read(./baml_client/**)"
    - "vendor/**"
`
	if err := os.WriteFile(filepath.Join(dir, ".iguana", "settings.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := LoadSettings(dir)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil settings")
	}
	if len(s.Permissions.Deny) != 2 {
		t.Fatalf("expected 2 deny rules, got %d", len(s.Permissions.Deny))
	}
	if !s.IsDenied("baml_client/foo.go") {
		t.Error("baml_client/foo.go should be denied")
	}
	if !s.IsDenied("vendor/foo.go") {
		t.Error("vendor/foo.go should be denied")
	}
	if s.IsDenied("main.go") {
		t.Error("main.go should not be denied")
	}
}

func TestLoadSettings_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".iguana"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".iguana", "settings.yaml"), []byte(":\tbad yaml:"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSettings(dir)
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}

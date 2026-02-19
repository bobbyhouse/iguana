package main

// settings.go — iguana configuration loaded from .iguana/settings.yaml.
//
// The settings file mirrors Claude Code's permission model: a deny list of
// glob patterns that controls which files iguana reads. Patterns may be
// written as bare globs ("baml_client/**") or wrapped in a Read() verb
// ("Read(./baml_client/**)") for familiarity.
//
// See INVARIANT.md INV-37.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Settings holds iguana configuration from .iguana/settings.yaml.
type Settings struct {
	Permissions Permissions `yaml:"permissions"`
}

// Permissions controls which files iguana reads.
type Permissions struct {
	// Deny is a list of glob patterns for files iguana should not read.
	// Patterns may be bare globs or wrapped in Read(...).
	// Example: ["Read(./baml_client/**)"]
	Deny []string `yaml:"deny"`
}

// LoadSettings reads .iguana/settings.yaml relative to root.
// Returns nil (not an error) if the file does not exist.
func LoadSettings(root string) (*Settings, error) {
	path := filepath.Join(root, ".iguana", "settings.yaml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var s Settings
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return &s, nil
}

// IsDenied reports whether relPath (forward-slash, relative to root) matches
// any deny rule. Safe to call on a nil *Settings receiver.
func (s *Settings) IsDenied(relPath string) bool {
	if s == nil {
		return false
	}
	for _, rule := range s.Permissions.Deny {
		if matchDenyPattern(parseDenyRule(rule), relPath) {
			return true
		}
	}
	return false
}

// parseDenyRule extracts the path glob from a deny rule.
//
//	"Read(./baml_client/**)" → "baml_client/**"
//	"baml_client/**"         → "baml_client/**"
func parseDenyRule(rule string) string {
	if strings.HasPrefix(rule, "Read(") && strings.HasSuffix(rule, ")") {
		rule = rule[5 : len(rule)-1]
	}
	return strings.TrimPrefix(rule, "./")
}

// matchDenyPattern reports whether path matches a deny glob pattern.
//
// "prefix/**" matches the prefix directory itself and every path beneath it.
// All other patterns use filepath.Match semantics (single * does not cross /).
func matchDenyPattern(pattern, path string) bool {
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}
	matched, _ := filepath.Match(pattern, path)
	return matched
}

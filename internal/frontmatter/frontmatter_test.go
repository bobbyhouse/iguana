package frontmatter_test

import (
	"testing"

	"iguana/internal/frontmatter"
)

func TestParseRoundtrip(t *testing.T) {
	type meta struct {
		Plugin string `yaml:"plugin"`
		Hash   string `yaml:"hash"`
	}

	m := meta{Plugin: "static", Hash: "abc123"}
	body := "# Hello\n\nworld\n"

	data, err := frontmatter.Write(m, body)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	fmBytes, bodyBytes, err := frontmatter.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_ = fmBytes
	if string(bodyBytes) != body {
		t.Errorf("body mismatch: got %q want %q", bodyBytes, body)
	}
}

func TestParseMissingOpen(t *testing.T) {
	_, _, err := frontmatter.Parse([]byte("no delimiter"))
	if err == nil {
		t.Fatal("expected error for missing opening delimiter")
	}
}

func TestParseMissingClose(t *testing.T) {
	_, _, err := frontmatter.Parse([]byte("---\nplugin: static\n"))
	if err == nil {
		t.Fatal("expected error for missing closing delimiter")
	}
}

func TestWriteNoBody(t *testing.T) {
	type meta struct {
		X int `yaml:"x"`
	}
	data, err := frontmatter.Write(meta{X: 1}, "")
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty output")
	}
}

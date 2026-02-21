// Package frontmatter provides helpers for reading and writing markdown files
// that carry YAML frontmatter between --- delimiters.
package frontmatter

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Parse splits a markdown document into its frontmatter (raw YAML bytes) and
// body. The document must begin with "---\n"; the closing "---" line ends the
// frontmatter block. Returns an error if the opening delimiter is absent.
func Parse(data []byte) (frontmatter []byte, body []byte, err error) {
	const delim = "---\n"
	if !bytes.HasPrefix(data, []byte(delim)) {
		return nil, nil, fmt.Errorf("frontmatter: missing opening --- delimiter")
	}
	rest := data[len(delim):]
	idx := bytes.Index(rest, []byte("\n---"))
	if idx < 0 {
		return nil, nil, fmt.Errorf("frontmatter: missing closing --- delimiter")
	}
	fm := rest[:idx]
	// Skip past closing delimiter and optional newline.
	tail := rest[idx+4:]
	if len(tail) > 0 && tail[0] == '\n' {
		tail = tail[1:]
	}
	return fm, tail, nil
}

// Write marshals v as YAML frontmatter and concatenates body, returning the
// complete markdown document with --- delimiters.
func Write(v any, body string) ([]byte, error) {
	fm, err := yaml.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("frontmatter: marshal: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(fm)
	buf.WriteString("---\n")
	if body != "" {
		buf.WriteString(body)
	}
	return buf.Bytes(), nil
}

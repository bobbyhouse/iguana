package main

// categorize.go — Classifies a Go file's state type via the TypeOfState LLM function.
//
// The classifierFunc variable is swappable so unit tests can inject a
// deterministic mock instead of calling the real LLM (INV-53, INV-56).

import (
	"context"
	"os"

	b "iguana/baml_client"
	"iguana/baml_client/types"
)

// classifierFunc is the signature of the LLM-backed state classifier.
type classifierFunc func(ctx context.Context, content string) (types.State, error)

// typeOfState is the active classifier; tests may replace it with a mock.
// The closure drops the variadic opts since categorizeFile does not need them.
var typeOfState classifierFunc = func(ctx context.Context, content string) (types.State, error) {
	return b.TypeOfState(ctx, content)
}

// categorizeFile reads the Go source file at filePath and returns its
// classified state type (INV-53..55).
func categorizeFile(filePath string) (types.State, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return typeOfState(context.Background(), string(content))
}

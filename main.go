package main

import (
	b "iguana/baml_client"
	"iguana/baml_client/types"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// categorizeFile reads a file and determines its state type.
func categorizeFile(filePath string) (types.State, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	ctx := context.Background()
	return b.TypeOfState(ctx, string(content))
}

type EvidenceBundle struct {
	Version int      `yaml:"version"`
	File    FileMeta `yaml:"file"`
}

type FileMeta struct {
	Path   string `yaml:"path"`
	SHA256 string `yaml:"sha256"`
}

func createEvidenceBundle(filePath string) (EvidenceBundle, error) {
	b, err := os.ReadFile(filePath)
	if err != nil {
		return EvidenceBundle{}, err
	}

	sum := sha256.Sum256(b)
	return EvidenceBundle{
		Version: 1,
		File: FileMeta{
			Path:   filePath,
			SHA256: hex.EncodeToString(sum[:]),
		},
	}, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <file-path>\n", os.Args[0])
		os.Exit(1)
	}

	// system-model subcommand: aggregate evidence bundles into a system model.
	if len(os.Args) >= 3 && os.Args[1] == "system-model" {
		root := os.Args[2]
		outputPath := filepath.Join(root, "system_model.yaml")
		if len(os.Args) >= 4 {
			outputPath = os.Args[3]
		}
		model, err := GenerateSystemModel(context.Background(), root)
		if err != nil {
			log.Fatal(err)
		}
		if err := WriteSystemModel(model, outputPath); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("wrote %s (%d state domains, %d effects)\n",
			outputPath, len(model.StateDomains), len(model.Effects))
		return
	}

	filePath := os.Args[1]

	// Directory mode: walk all .go files under the root.
	if info, err := os.Stat(filePath); err == nil && info.IsDir() {
		written, errs := walkAndGenerate(filePath)
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "error: %v\n", e)
		}
		fmt.Printf("wrote %d bundles, %d errors\n", written, len(errs))
		if len(errs) > 0 {
			os.Exit(1)
		}
		return
	}

	if strings.HasSuffix(filePath, ".go") {
		// v2: semantic analysis — writes companion .evidence.yaml file.
		bundle, err := createEvidenceBundleV2(filePath)
		if err != nil {
			log.Fatal(err)
		}
		if err := writeEvidenceBundleV2(bundle); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("wrote %s.evidence.yaml\n", filePath)
		return
	}

	// v1: integrity only — prints to stdout.
	evidence, err := createEvidenceBundle(filePath)
	if err != nil {
		log.Fatal(err)
	}
	out, err := yaml.Marshal(evidence)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(string(out))
}

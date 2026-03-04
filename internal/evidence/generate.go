package evidence

// generate.go — Evidence bundle generation: analysis orchestration and
// directory walking.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"iguana/internal/settings"
)

// ---------------------------------------------------------------------------
// Public API — Generation
// ---------------------------------------------------------------------------

// CreateEvidenceBundle performs pure static analysis on a Go source file
// and returns an evidence bundle. It does not write any files (INV-20).
//
// It first attempts to load the package with full type information via
// golang.org/x/tools/go/packages. On failure it falls back to AST-only
// analysis — call targets and type strings are then best-effort.
func CreateEvidenceBundle(filePath string) (*EvidenceBundle, error) {
	// Step 1 — integrity (same as v1): read raw bytes, compute hash.
	fileBytes, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	sum := sha256.Sum256(fileBytes)
	hash := hex.EncodeToString(sum[:])
	normalizedPath := filepath.ToSlash(filePath)

	// Step 2 — parse + type-load.
	// Try the richer path (go/packages) first; fall back to go/parser.
	file, typesInfo, typesPkg, err := loadTypeInfoForFile(filePath)
	if err != nil {
		// Fall back: parse with go/parser, no type info.
		fset := token.NewFileSet()
		file, err = parser.ParseFile(fset, filePath, nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse: %w", err)
		}
		typesInfo = nil
		typesPkg = nil
	}

	return buildBundle(normalizedPath, hash, file, typesInfo, typesPkg), nil
}

// buildBundle assembles an EvidenceBundle from pre-loaded AST and type data.
// normalizedPath is already slash-normalized; hash is the hex-encoded SHA256.
// typesInfo and typesPkg may be nil (AST-only fallback).
func buildBundle(normalizedPath, hash string, file *ast.File, typesInfo *types.Info, typesPkg *types.Package) *EvidenceBundle {
	qualifier := makeQualifier(typesPkg)
	pkgMeta := extractPackageMeta(file)
	syms := extractSymbols(file, typesInfo, typesPkg, qualifier)
	calls := extractCalls(file, typesInfo, typesPkg, qualifier)
	sigs := extractSignals(pkgMeta, calls, file)

	return &EvidenceBundle{
		Version: 2,
		File: FileMeta{
			Path:   normalizedPath,
			SHA256: hash,
		},
		Package: pkgMeta,
		Symbols: syms,
		Calls:   calls,
		Signals: sigs,
	}
}

// ---------------------------------------------------------------------------
// Directory Walking
// ---------------------------------------------------------------------------

// WalkAndGenerate walks root recursively, generating an evidence bundle for
// every .go file found. Directories named vendor, testdata, or starting with
// "." are skipped entirely (INV-24). Directories and files are processed in
// sorted order (INV-25). Each directory's package is loaded once (INV-26).
//
// If force is false, files whose existing bundle SHA256 matches the current
// source are skipped (INV-50). Returns counts of written and skipped files.
func WalkAndGenerate(root string, force bool) (written, skipped int, errs []error) {
	s, err := settings.LoadSettings(root)
	if err != nil {
		errs = append(errs, fmt.Errorf("load settings: %w", err))
		return
	}

	// Collect .go files grouped by directory.
	filesByDir := make(map[string][]string)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()

		// Compute the forward-slash relative path for settings checks.
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)

		if d.IsDir() {
			// Always descend into the root itself.
			if path == root {
				return nil
			}
			// Skip vendor, testdata, examples, docs, and hidden directories (INV-24).
			if name == "vendor" || name == "testdata" || name == "examples" || name == "docs" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			// Skip directories denied by settings (INV-39).
			if s.IsDenied(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(name) != ".go" {
			return nil
		}
		// Skip test files (INV-24).
		if strings.HasSuffix(name, "_test.go") {
			return nil
		}
		// Skip files denied by settings (INV-39).
		if s.IsDenied(rel) {
			return nil
		}
		dir := filepath.Dir(path)
		filesByDir[dir] = append(filesByDir[dir], path)
		return nil
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("walk %s: %w", root, err))
		return
	}

	// Sort directories for deterministic processing (INV-25).
	dirs := make([]string, 0, len(filesByDir))
	for dir := range filesByDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	for _, dir := range dirs {
		files := filesByDir[dir]
		sort.Strings(files) // sort files within each dir (INV-25)

		// Load the package once per directory (INV-26).
		// pkg may be nil if loading fails; buildBundleForFile falls back to go/parser.
		pkg, fset, _ := loadPackageForDir(dir)

		for _, absPath := range files {
			relPath, err := filepath.Rel(root, absPath)
			if err != nil {
				errs = append(errs, fmt.Errorf("rel path %s: %w", absPath, err))
				continue
			}
			relPath = filepath.ToSlash(relPath)

			bundle, err := buildBundleForFile(absPath, relPath, pkg, fset)
			if err != nil {
				errs = append(errs, fmt.Errorf("build bundle %s: %w", relPath, err))
				continue
			}

			sk, err := writeBundleAt(bundle, absPath, force)
			if err != nil {
				errs = append(errs, fmt.Errorf("write bundle %s: %w", relPath, err))
				continue
			}
			if sk {
				skipped++
			} else {
				written++
			}
		}
	}
	return
}

// buildBundleForFile creates an EvidenceBundle for a single file.
// It uses the pre-loaded pkg/fset when the file can be found in pkg.Syntax;
// otherwise it falls back to go/parser with no type information.
// absPath is the absolute filesystem path; relPath is the root-relative
// forward-slash path stored as file.path in the bundle (INV-23).
func buildBundleForFile(absPath, relPath string, pkg *packages.Package, fset *token.FileSet) (*EvidenceBundle, error) {
	fileBytes, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	sum := sha256.Sum256(fileBytes)
	hash := hex.EncodeToString(sum[:])

	// Try to find the file in the pre-loaded package syntax.
	if pkg != nil && fset != nil && pkg.TypesInfo != nil && pkg.Types != nil {
		for _, f := range pkg.Syntax {
			pos := fset.Position(f.Pos())
			if pos.Filename == absPath {
				return buildBundle(relPath, hash, f, pkg.TypesInfo, pkg.Types), nil
			}
		}
	}

	// Fall back to go/parser (no type info).
	fileFset := token.NewFileSet()
	file, err := parser.ParseFile(fileFset, absPath, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return buildBundle(relPath, hash, file, nil, nil), nil
}

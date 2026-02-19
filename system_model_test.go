package main

// system_model_test.go — Deterministic tests for system model generation.
//
// All tests here are deterministic — no LLM calls are made.
// Tests cover: loadEvidenceBundles, buildInventory, buildBoundaries,
// buildEffects, computeBundleSetHash, and evidenceRef.
//
// Invariants tested (see INVARIANT.md INV-27..31):
//   INV-27  system_model.yaml derived from evidence bundles
//   INV-28  all arrays sorted alphabetically
//   INV-29  inferred elements have evidence_refs (structural test only)
//   INV-30  evidence refs follow exact format
//   INV-31  bundle_set_sha256 derived from all bundle hashes

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeTestBundle constructs a minimal EvidenceBundle for testing.
func makeTestBundle(path, sha256, pkgName string, signals Signals) *EvidenceBundle {
	return &EvidenceBundle{
		Version: 2,
		File: FileMeta{
			Path:   path,
			SHA256: sha256,
		},
		Package: PackageMeta{Name: pkgName},
		Signals: signals,
	}
}

// writeTestBundle writes a minimal evidence YAML to dir/<name>.evidence.yaml.
// Returns the path to the written file.
func writeTestBundle(t *testing.T, dir, name string, bundle *EvidenceBundle) string {
	t.Helper()
	data, err := yaml.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal test bundle: %v", err)
	}
	path := filepath.Join(dir, name+".evidence.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write test bundle: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------
// Unit tests — evidenceRef (INV-30)
// ---------------------------------------------------------------------------

// TestEvidenceRefFormat verifies evidenceRef returns the correct string per spec.
// INV-30: evidence refs follow exactly bundle:<path>@v<version>[#<fragment>]
func TestEvidenceRefFormat(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		version  int
		fragment string
		want     string
	}{
		{
			name:     "no fragment",
			path:     "pkg/auth/auth.go",
			version:  2,
			fragment: "",
			want:     "bundle:pkg/auth/auth.go@v2",
		},
		{
			name:     "symbol fragment",
			path:     "main.go",
			version:  2,
			fragment: "symbol:main",
			want:     "bundle:main.go@v2#symbol:main",
		},
		{
			name:     "signal fragment",
			path:     "server/server.go",
			version:  2,
			fragment: "signal:db_calls",
			want:     "bundle:server/server.go@v2#signal:db_calls",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evidenceRef(tt.path, tt.version, tt.fragment)
			if got != tt.want {
				t.Errorf("evidenceRef(%q, %d, %q) = %q, want %q",
					tt.path, tt.version, tt.fragment, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Unit tests — loadEvidenceBundles (INV-27, INV-31)
// ---------------------------------------------------------------------------

// TestLoadEvidenceBundles_Empty verifies that a directory with no YAML files
// returns 0 bundles and no error.
func TestLoadEvidenceBundles_Empty(t *testing.T) {
	dir := t.TempDir()

	bundles, err := loadEvidenceBundles(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(bundles) != 0 {
		t.Errorf("expected 0 bundles, got %d", len(bundles))
	}
}

// TestLoadEvidenceBundles_Basic writes a minimal evidence YAML, loads it, and
// verifies the bundle fields are round-tripped correctly (INV-27).
func TestLoadEvidenceBundles_Basic(t *testing.T) {
	dir := t.TempDir()

	bundle := makeTestBundle("pkg/foo.go", "abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234", "foo", Signals{FSReads: true})
	writeTestBundle(t, dir, "foo.go", bundle)

	bundles, err := loadEvidenceBundles(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected 1 bundle, got %d", len(bundles))
	}
	got := bundles[0]
	if got.File.Path != bundle.File.Path {
		t.Errorf("Path = %q, want %q", got.File.Path, bundle.File.Path)
	}
	if got.Package.Name != bundle.Package.Name {
		t.Errorf("Package.Name = %q, want %q", got.Package.Name, bundle.Package.Name)
	}
	if !got.Signals.FSReads {
		t.Error("expected Signals.FSReads = true")
	}
}

// ---------------------------------------------------------------------------
// Unit tests — computeBundleSetHash (INV-31)
// ---------------------------------------------------------------------------

// TestComputeBundleSetHash_Deterministic verifies that feeding the same bundles
// in a different order produces the same hash (INV-31).
func TestComputeBundleSetHash_Deterministic(t *testing.T) {
	b1 := makeTestBundle("a.go", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1", "main", Signals{})
	b2 := makeTestBundle("b.go", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2", "main", Signals{})

	hash1 := computeBundleSetHash([]*EvidenceBundle{b1, b2})
	hash2 := computeBundleSetHash([]*EvidenceBundle{b2, b1})

	if hash1 != hash2 {
		t.Errorf("hash depends on order: %q vs %q", hash1, hash2)
	}
	if len(hash1) != 64 {
		t.Errorf("expected 64-char hex hash, got %d chars: %q", len(hash1), hash1)
	}
}

// ---------------------------------------------------------------------------
// Unit tests — buildInventory (INV-28)
// ---------------------------------------------------------------------------

// TestBuildInventory_GroupsByPackage verifies that two bundles in the same
// package produce a single inventory entry (INV-28).
func TestBuildInventory_GroupsByPackage(t *testing.T) {
	b1 := makeTestBundle("pkg/foo.go", "a", "auth", Signals{})
	b2 := makeTestBundle("pkg/bar.go", "b", "auth", Signals{})

	inv := buildInventory([]*EvidenceBundle{b1, b2})

	if len(inv.Packages) != 1 {
		t.Fatalf("expected 1 package, got %d", len(inv.Packages))
	}
	pkg := inv.Packages[0]
	if pkg.Name != "auth" {
		t.Errorf("Name = %q, want %q", pkg.Name, "auth")
	}
	if len(pkg.Files) != 2 {
		t.Errorf("Files count = %d, want 2", len(pkg.Files))
	}
	// Files must be sorted (INV-28).
	if pkg.Files[0] != "pkg/bar.go" || pkg.Files[1] != "pkg/foo.go" {
		t.Errorf("files not sorted: %v", pkg.Files)
	}
}

// TestBuildInventory_Entrypoints verifies that a package=main bundle with a
// main function symbol is identified as an entrypoint.
func TestBuildInventory_Entrypoints(t *testing.T) {
	b1 := &EvidenceBundle{
		Version: 2,
		File:    FileMeta{Path: "main.go", SHA256: "a"},
		Package: PackageMeta{Name: "main"},
		Symbols: Symbols{
			Functions: []Function{{Name: "main", Exported: false}},
		},
	}

	inv := buildInventory([]*EvidenceBundle{b1})

	if len(inv.Entrypoints) != 1 {
		t.Fatalf("expected 1 entrypoint, got %d", len(inv.Entrypoints))
	}
	ep := inv.Entrypoints[0]
	if ep.Symbol != "main" {
		t.Errorf("Symbol = %q, want %q", ep.Symbol, "main")
	}
	if len(ep.EvidenceRefs) == 0 {
		t.Error("expected at least one evidence_ref on entrypoint")
	}
}

// ---------------------------------------------------------------------------
// Unit tests — buildBoundaries
// ---------------------------------------------------------------------------

// TestBuildBoundaries_DBCalls verifies that a bundle with DBCalls=true produces
// a db persistence boundary entry.
func TestBuildBoundaries_DBCalls(t *testing.T) {
	bnd := makeTestBundle("store/db.go", "x", "store", Signals{DBCalls: true})

	boundaries := buildBoundaries([]*EvidenceBundle{bnd})

	if len(boundaries.Persistence) == 0 {
		t.Fatal("expected at least one persistence boundary")
	}
	found := false
	for _, p := range boundaries.Persistence {
		if p.Kind == "db" {
			found = true
			if len(p.Writers) == 0 {
				t.Error("expected at least one db writer")
			}
		}
	}
	if !found {
		t.Error("expected db persistence boundary")
	}
}

// TestBuildBoundaries_NetCalls verifies that a bundle with NetCalls=true
// produces a network.outbound entry.
func TestBuildBoundaries_NetCalls(t *testing.T) {
	bnd := makeTestBundle("client/http.go", "x", "client", Signals{NetCalls: true})

	boundaries := buildBoundaries([]*EvidenceBundle{bnd})

	if boundaries.Network == nil {
		t.Fatal("expected network boundary, got nil")
	}
	if len(boundaries.Network.Outbound) == 0 {
		t.Error("expected at least one outbound entry")
	}
}

// ---------------------------------------------------------------------------
// Unit tests — buildEffects (INV-28)
// ---------------------------------------------------------------------------

// TestBuildEffects_FromSignals verifies that each signal kind produces the
// correct effect kind.
func TestBuildEffects_FromSignals(t *testing.T) {
	bundles := []*EvidenceBundle{
		makeTestBundle("db.go", "a", "store", Signals{DBCalls: true}),
		makeTestBundle("fs.go", "b", "io", Signals{FSReads: true, FSWrites: true}),
		makeTestBundle("net.go", "c", "http", Signals{NetCalls: true}),
	}

	effects := buildEffects(bundles)

	kinds := make(map[string]bool)
	for _, e := range effects {
		kinds[e.Kind] = true
	}

	for _, want := range []string{"db_write", "fs_read", "fs_write", "net_call"} {
		if !kinds[want] {
			t.Errorf("missing effect kind %q", want)
		}
	}
}

// TestBuildEffects_Sorted verifies effects are sorted by kind then via (INV-28).
func TestBuildEffects_Sorted(t *testing.T) {
	bundles := []*EvidenceBundle{
		makeTestBundle("z.go", "a", "pkg", Signals{FSReads: true, NetCalls: true}),
		makeTestBundle("a.go", "b", "pkg", Signals{FSReads: true, DBCalls: true}),
	}

	effects := buildEffects(bundles)

	for i := 1; i < len(effects); i++ {
		prev, curr := effects[i-1], effects[i]
		if curr.Kind < prev.Kind {
			t.Errorf("effects not sorted by kind at %d: %q < %q", i, curr.Kind, prev.Kind)
		}
		if curr.Kind == prev.Kind && curr.Via < prev.Via {
			t.Errorf("effects not sorted by via at %d: %q < %q", i, curr.Via, prev.Via)
		}
	}
}

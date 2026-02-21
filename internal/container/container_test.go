package container_test

import (
	"os"
	"path/filepath"
	"testing"

	"iguana/internal/container"
)

// setHome redirects os.UserHomeDir to a temp directory for the duration of the test.
func withTempHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	return tmp
}

func TestInitAndOpen(t *testing.T) {
	tmp := withTempHome(t)

	if err := container.Init("mycontainer"); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Directory must exist.
	dir := filepath.Join(tmp, ".iguana", "mycontainer")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("container dir not created: %v", err)
	}

	// Init again must fail.
	if err := container.Init("mycontainer"); err == nil {
		t.Fatal("expected error on duplicate Init")
	}

	c, err := container.Open("mycontainer")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if c.Dir != dir {
		t.Errorf("Dir mismatch: got %s want %s", c.Dir, dir)
	}
}

func TestOpenMissing(t *testing.T) {
	withTempHome(t)
	_, err := container.Open("notexist")
	if err == nil {
		t.Fatal("expected error for missing container")
	}
}

func TestAddProjectAndLoad(t *testing.T) {
	withTempHome(t)
	if err := container.Init("c"); err != nil {
		t.Fatal(err)
	}
	c, _ := container.Open("c")

	cfg := container.ProjectConfig{
		Plugins: map[string]map[string]string{
			"static": {"repository": "https://github.com/org/repo"},
		},
	}
	if err := c.AddProject("proj", cfg); err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	// Duplicate must fail.
	if err := c.AddProject("proj", cfg); err == nil {
		t.Fatal("expected error on duplicate AddProject")
	}

	got, err := c.LoadProject("proj")
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if got.Plugins["static"]["repository"] != "https://github.com/org/repo" {
		t.Errorf("unexpected config: %+v", got)
	}
}

func TestChangeRequest(t *testing.T) {
	tmp := withTempHome(t)

	if err := container.Init("c"); err != nil {
		t.Fatal(err)
	}
	c, _ := container.Open("c")

	cfg := container.ProjectConfig{
		Plugins: map[string]map[string]string{
			"static": {"repository": "https://github.com/org/repo"},
		},
	}
	if err := c.AddProject("proj", cfg); err != nil {
		t.Fatal(err)
	}

	// Create an evidence file to ensure recursive copy works.
	evidenceDir := filepath.Join(c.Dir, "proj", "static")
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evidenceDir, "result.json"), []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(tmp, "exported")
	if err := c.ChangeRequest(dst, "Fix the auth bug"); err != nil {
		t.Fatalf("ChangeRequest: %v", err)
	}

	crDir := filepath.Join(dst, ".tmp", "change-request")

	// Config yaml must NOT be present.
	if _, err := os.Stat(filepath.Join(crDir, "proj.yaml")); err == nil {
		t.Error("proj.yaml should not be in change request")
	}

	// Evidence file must exist at the flattened path.
	data, err := os.ReadFile(filepath.Join(crDir, "proj-static", "result.json"))
	if err != nil {
		t.Fatalf("expected evidence file in change request: %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Errorf("unexpected content: %s", data)
	}

	// index.md must contain the description.
	indexData, err := os.ReadFile(filepath.Join(crDir, "index.md"))
	if err != nil {
		t.Fatalf("expected index.md: %v", err)
	}
	if string(indexData) != "Fix the auth bug" {
		t.Errorf("unexpected index.md content: %s", indexData)
	}
}

func TestChangeRequestMultipleProjects(t *testing.T) {
	tmp := withTempHome(t)

	if err := container.Init("c"); err != nil {
		t.Fatal(err)
	}
	c, _ := container.Open("c")

	for _, proj := range []string{"alpha", "beta"} {
		cfg := container.ProjectConfig{
			Plugins: map[string]map[string]string{
				"static": {"repository": "https://example.com/" + proj},
			},
		}
		if err := c.AddProject(proj, cfg); err != nil {
			t.Fatal(err)
		}
		evidenceDir := filepath.Join(c.Dir, proj, "static")
		if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(evidenceDir, "data.txt"), []byte(proj), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	dst := filepath.Join(tmp, "out")
	if err := c.ChangeRequest(dst, "multi-project change"); err != nil {
		t.Fatalf("ChangeRequest: %v", err)
	}

	crDir := filepath.Join(dst, ".tmp", "change-request")

	// Both flattened dirs must exist.
	for _, name := range []string{"alpha-static", "beta-static"} {
		if _, err := os.Stat(filepath.Join(crDir, name)); err != nil {
			t.Errorf("expected dir %s: %v", name, err)
		}
	}
}

func TestChangeRequestCreatesTmpDir(t *testing.T) {
	tmp := withTempHome(t)

	if err := container.Init("c"); err != nil {
		t.Fatal(err)
	}
	c, _ := container.Open("c")

	// Destination doesn't exist yet — ChangeRequest should create it along with .tmp/.
	dst := filepath.Join(tmp, "newdir")
	if err := c.ChangeRequest(dst, "hello"); err != nil {
		t.Fatalf("ChangeRequest: %v", err)
	}

	tmpDir := filepath.Join(dst, ".tmp")
	if _, err := os.Stat(tmpDir); err != nil {
		t.Errorf("expected .tmp dir to be created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "change-request")); err != nil {
		t.Errorf("expected change-request dir inside .tmp: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "change-request", "index.md")); err != nil {
		t.Errorf("expected index.md: %v", err)
	}
}

func TestChangeRequestTargetAlreadyExists(t *testing.T) {
	tmp := withTempHome(t)

	if err := container.Init("c"); err != nil {
		t.Fatal(err)
	}
	c, _ := container.Open("c")

	dst := filepath.Join(tmp, "exported")
	// Pre-create the target directory at dst/.tmp/change-request.
	if err := os.MkdirAll(filepath.Join(dst, ".tmp", "change-request"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := c.ChangeRequest(dst, "desc"); err == nil {
		t.Fatal("expected error when change-request target already exists")
	}
}

func TestChangeRequestSkipsProjectsWithNoEvidence(t *testing.T) {
	tmp := withTempHome(t)

	if err := container.Init("c"); err != nil {
		t.Fatal(err)
	}
	c, _ := container.Open("c")

	// Add project with config but no evidence directory.
	cfg := container.ProjectConfig{
		Plugins: map[string]map[string]string{
			"static": {"repository": "https://example.com/repo"},
		},
	}
	if err := c.AddProject("empty", cfg); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(tmp, "out")
	if err := c.ChangeRequest(dst, "empty project"); err != nil {
		t.Fatalf("ChangeRequest: %v", err)
	}

	crDir := filepath.Join(dst, ".tmp", "change-request")

	// Only index.md should exist, no evidence dirs.
	entries, err := os.ReadDir(crDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "index.md" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("expected only index.md, got %v", names)
	}
}

func TestListProjects(t *testing.T) {
	withTempHome(t)
	if err := container.Init("c"); err != nil {
		t.Fatal(err)
	}
	c, _ := container.Open("c")

	names, err := c.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Fatalf("expected 0 projects, got %d", len(names))
	}

	c.AddProject("alpha", container.ProjectConfig{Plugins: map[string]map[string]string{}})
	c.AddProject("beta", container.ProjectConfig{Plugins: map[string]map[string]string{}})

	names, err = c.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 projects, got %d: %v", len(names), names)
	}
}

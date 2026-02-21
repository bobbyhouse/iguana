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

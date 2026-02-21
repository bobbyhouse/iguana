// Package container manages the ~/.iguana/ directory hierarchy.
//
// Directory layout:
//
//	~/.iguana/<container>/
//	    <project>.yaml           # project config: plugin name -> key/value map
//	    <project>/<plugin>/      # evidence bundles produced by that plugin
package container

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Container represents a named iguana container directory (~/.iguana/<name>/).
type Container struct {
	Dir string
}

// ProjectConfig stores per-plugin configuration for a project.
// Keys are plugin names; values are config key/value maps.
type ProjectConfig struct {
	Plugins map[string]map[string]string `yaml:"plugins"`
}

// igDir returns the base ~/.iguana directory.
func igDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".iguana"), nil
}

// Init creates ~/.iguana/<name>/ and errors if it already exists.
func Init(name string) error {
	base, err := igDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(base, name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("container %q already exists at %s", name, dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	return nil
}

// Open opens an existing container directory. Returns an error if not found.
func Open(name string) (*Container, error) {
	base, err := igDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, name)
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("container %q not found (run 'iguana init %s' first)", name, name)
	}
	return &Container{Dir: dir}, nil
}

// projectPath returns the path to <project>.yaml inside the container.
func (c *Container) projectPath(name string) string {
	return filepath.Join(c.Dir, name+".yaml")
}

// AddProject writes a project config file. Errors if it already exists.
func (c *Container) AddProject(name string, config ProjectConfig) error {
	path := c.projectPath(name)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("project %q already exists in container", name)
	}
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal project config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write project config: %w", err)
	}
	return nil
}

// LoadProject reads and parses a project config file.
func (c *Container) LoadProject(name string) (*ProjectConfig, error) {
	data, err := os.ReadFile(c.projectPath(name))
	if err != nil {
		return nil, fmt.Errorf("read project %q: %w", name, err)
	}
	var cfg ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse project %q: %w", name, err)
	}
	return &cfg, nil
}

// ListProjects returns project names derived from *.yaml files in the container.
func (c *Container) ListProjects() ([]string, error) {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return nil, fmt.Errorf("read container dir: %w", err)
	}
	var projects []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".yaml") {
			projects = append(projects, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	return projects, nil
}

// RemoveProject removes a project's config file and evidence directory.
func (c *Container) RemoveProject(name string) error {
	path := c.projectPath(name)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("project %q not found in container", name)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove project config: %w", err)
	}
	// Remove evidence directory if it exists.
	evidenceDir := filepath.Join(c.Dir, name)
	if _, err := os.Stat(evidenceDir); err == nil {
		if err := os.RemoveAll(evidenceDir); err != nil {
			return fmt.Errorf("remove project evidence: %w", err)
		}
	}
	return nil
}

// List returns the names of all containers under ~/.iguana/.
func List() ([]string, error) {
	base, err := igDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read iguana dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// ChangeRequest writes a flattened copy of the container's evidence into
// dst/.tmp/change-request/. Config .yaml files are excluded. Each
// <project>/<plugin>/ evidence directory is flattened to
// <project>-<plugin>/. The provided description is written to index.md.
//
// Creates dst/.tmp/ if it doesn't exist. Errors if the target directory
// already exists.
func (c *Container) ChangeRequest(dst, description string) error {
	tmpDir := filepath.Join(dst, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("create .tmp dir: %w", err)
	}
	target := filepath.Join(tmpDir, "change-request")
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("change-request target %q already exists", target)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create change-request dir: %w", err)
	}

	projects, err := c.ListProjects()
	if err != nil {
		return err
	}

	for _, proj := range projects {
		projDir := filepath.Join(c.Dir, proj)
		entries, err := os.ReadDir(projDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read project dir %q: %w", proj, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			pluginName := e.Name()
			flatName := proj + "-" + pluginName
			src := filepath.Join(projDir, pluginName)
			dst := filepath.Join(target, flatName)
			if err := copyDir(src, dst); err != nil {
				return fmt.Errorf("copy %s: %w", flatName, err)
			}
		}
	}

	if err := os.WriteFile(filepath.Join(target, "index.md"), []byte(description), 0o644); err != nil {
		return fmt.Errorf("write index.md: %w", err)
	}
	return nil
}

// copyDir recursively copies src to dst.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target)
	})
}

// copyFile copies a single file from src to dst, preserving permissions.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// Remove deletes a container and all its contents.
func Remove(name string) error {
	base, err := igDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(base, name)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("container %q not found", name)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove container: %w", err)
	}
	return nil
}

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

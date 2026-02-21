package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"iguana/internal/container"
	"iguana/internal/plugin"
	"iguana/internal/static"
)

// command describes a CLI subcommand.
type command struct {
	name  string
	short string
	usage string
	long  string
	run   func(args []string) error
}

var commands = []command{
	{
		name:  "init",
		short: "Create a new iguana container",
		usage: "iguana init <name>",
		long: `Create a new iguana container at ~/.iguana/<name>/.

Errors if the container already exists.
`,
		run: runInit,
	},
	{
		name:  "add",
		short: "Add a project to a container",
		usage: "iguana add <container> <project>",
		long: `Add a new project to an existing container.

Prompts for configuration (e.g. git repository URL) and writes
~/.iguana/<container>/<project>.yaml.

Errors if the project already exists.
`,
		run: runAdd,
	},
	{
		name:  "analyze",
		short: "Run all plugins for every project in a container",
		usage: "iguana analyze <container>",
		long: `Run analysis for every project in the container.

For each project, runs configured plugins and writes evidence bundles to
~/.iguana/<container>/<project>/<plugin>/.
`,
		run: runAnalyze,
	},
}

// plugins is the registry of available evidence producers.
var plugins = map[string]plugin.EvidenceProducer{
	"static": &static.GoAnalyzer{},
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, "iguana — container-based evidence generation\n\n")
	fmt.Fprintf(w, "Usage:\n  iguana <command> [arguments]\n\n")
	fmt.Fprintf(w, "Commands:\n")
	for _, cmd := range commands {
		fmt.Fprintf(w, "  %-10s %s\n", cmd.name, cmd.short)
	}
	fmt.Fprintf(w, "\nRun 'iguana help <command>' for details on a specific command.\n")
}

func printCommandHelp(w io.Writer, name string) {
	for _, cmd := range commands {
		if cmd.name == name {
			fmt.Fprintf(w, "Usage: %s\n\n%s", cmd.usage, cmd.long)
			return
		}
	}
	fmt.Fprintf(w, "iguana: unknown command %q\n\nRun 'iguana help' for usage.\n", name)
}

func dispatch(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printUsage(os.Stdout)
		return nil
	}
	if args[0] == "help" {
		if len(args) >= 2 {
			printCommandHelp(os.Stdout, args[1])
		} else {
			printUsage(os.Stdout)
		}
		return nil
	}
	for _, cmd := range commands {
		if cmd.name == args[0] {
			return cmd.run(args[1:])
		}
	}
	return fmt.Errorf("unknown command %q\n\nRun 'iguana help' for usage.", args[0])
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

func runInit(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: iguana init <name>")
	}
	name := args[0]
	if err := container.Init(name); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	fmt.Printf("created container %q at %s\n", name, filepath.Join(home, ".iguana", name))
	return nil
}

// ---------------------------------------------------------------------------
// add
// ---------------------------------------------------------------------------

func runAdd(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: iguana add <container> <project>")
	}
	containerName := args[0]
	projectName := args[1]

	c, err := container.Open(containerName)
	if err != nil {
		return err
	}

	// For now iguana ships only the "static" plugin.
	producer := plugins["static"]
	questions, err := producer.Configure()
	if err != nil {
		return fmt.Errorf("configure plugin: %w", err)
	}

	answers, err := promptQuestions(questions)
	if err != nil {
		return fmt.Errorf("prompt: %w", err)
	}

	cfg := container.ProjectConfig{
		Plugins: map[string]map[string]string{
			producer.Name(): answers,
		},
	}
	if err := c.AddProject(projectName, cfg); err != nil {
		return err
	}
	fmt.Printf("added project %q to container %q\n", projectName, containerName)
	return nil
}

// ---------------------------------------------------------------------------
// analyze
// ---------------------------------------------------------------------------

func runAnalyze(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: iguana analyze <container>")
	}
	containerName := args[0]

	c, err := container.Open(containerName)
	if err != nil {
		return err
	}

	projects, err := c.ListProjects()
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		fmt.Printf("no projects in container %q\n", containerName)
		return nil
	}

	var anyErr bool
	for _, proj := range projects {
		cfg, err := c.LoadProject(proj)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error loading project %q: %v\n", proj, err)
			anyErr = true
			continue
		}
		for pluginName, pluginCfg := range cfg.Plugins {
			producer, ok := plugins[pluginName]
			if !ok {
				fmt.Fprintf(os.Stderr, "unknown plugin %q in project %q (skipping)\n", pluginName, proj)
				anyErr = true
				continue
			}
			outputDir := filepath.Join(c.Dir, proj, pluginName)
			fmt.Printf("analyzing %s/%s [%s]...\n", containerName, proj, pluginName)
			if err := producer.Analyze(pluginCfg, outputDir); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				anyErr = true
			} else {
				fmt.Printf("  done → %s\n", outputDir)
			}
		}
	}
	if anyErr {
		return fmt.Errorf("one or more errors during analysis")
	}
	return nil
}

// ---------------------------------------------------------------------------
// TUI prompt helpers
// ---------------------------------------------------------------------------

// promptModel is a bubbletea model that asks one question at a time.
type promptModel struct {
	questions []plugin.ConfigQuestion
	idx       int
	inputs    []textinput.Model
	done      bool
}

func newPromptModel(questions []plugin.ConfigQuestion) promptModel {
	inputs := make([]textinput.Model, len(questions))
	for i, q := range questions {
		ti := textinput.New()
		ti.Placeholder = q.Prompt
		ti.CharLimit = 512
		inputs[i] = ti
	}
	m := promptModel{
		questions: questions,
		inputs:    inputs,
	}
	if len(inputs) > 0 {
		m.inputs[0].Focus()
	}
	return m
}

func (m promptModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m promptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyEnter:
			if m.idx < len(m.inputs)-1 {
				m.inputs[m.idx].Blur()
				m.idx++
				m.inputs[m.idx].Focus()
				return m, textinput.Blink
			}
			m.done = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.inputs[m.idx], cmd = m.inputs[m.idx].Update(msg)
	return m, cmd
}

func (m promptModel) View() string {
	if m.done || len(m.questions) == 0 {
		return ""
	}
	q := m.questions[m.idx]
	return fmt.Sprintf("%s: %s\n", q.Prompt, m.inputs[m.idx].View())
}

// promptQuestions runs the TUI and returns answers keyed by ConfigQuestion.Key.
func promptQuestions(questions []plugin.ConfigQuestion) (map[string]string, error) {
	if len(questions) == 0 {
		return map[string]string{}, nil
	}
	m := newPromptModel(questions)
	p := tea.NewProgram(m)
	result, err := p.Run()
	if err != nil {
		return nil, err
	}
	final, ok := result.(promptModel)
	if !ok || !final.done {
		return nil, fmt.Errorf("prompt cancelled")
	}
	answers := make(map[string]string, len(questions))
	for i, q := range questions {
		answers[q.Key] = final.inputs[i].Value()
	}
	return answers, nil
}

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

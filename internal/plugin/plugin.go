package plugin

// ConfigQuestion describes a single configuration prompt for a plugin.
type ConfigQuestion struct {
	Key    string
	Prompt string
	Type   string // "text"
}

// EvidenceProducer is the interface every iguana plugin must implement.
type EvidenceProducer interface {
	// Name returns the plugin's canonical short identifier (e.g. "static").
	Name() string

	// Configure returns the questions the plugin needs answered before it can run.
	Configure() ([]ConfigQuestion, error)

	// Analyze runs the plugin using the provided config key/value pairs,
	// writing evidence bundles into outputDir.
	Analyze(config map[string]string, outputDir string) error
}

package canvas

type Trigger struct {
	Name  string `yaml:"name" json:"name"`
	Type  string `yaml:"type" json:"type"`
	Agent string `yaml:"agent" json:"agent"`
	Path  string `yaml:"path" json:"path,omitempty"`
}

package llm

// ModelConfig configures which model to use.
type ModelConfig struct {
	ModelName string
	FastMode  bool
	MaxTokens int
}

const (
	DefaultModel     = "deepseek-v4-pro"
	FastModel        = "deepseek-v4-flash"
	DefaultMaxTokens = 8192
)

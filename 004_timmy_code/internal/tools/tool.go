package tools

import (
	"context"

	"github.com/timmy/timmy-code/internal/schema"
)

// Tool is the interface every tool must implement.
type Tool interface {
	Name() string
	Description() string
	InputSchema() map[string]any
	Call(ctx context.Context, input map[string]any) (*Result, error)
}

// Result holds successful tool output. Errors use Go error return.
type Result struct {
	Output   string
	ExitCode int
}

// Registry manages available tools.
type Registry interface {
	GetAll() []Tool
	GetAllDefs() []schema.ToolDef
	FindByName(name string) (Tool, bool)
}

// NewRegistry creates a registry with the given tools.
func NewRegistry(tools ...Tool) Registry {
	return &registryImpl{tools: tools}
}

type registryImpl struct {
	tools []Tool
}

func (r *registryImpl) GetAll() []Tool { return r.tools }

func (r *registryImpl) GetAllDefs() []schema.ToolDef {
	defs := make([]schema.ToolDef, len(r.tools))
	for i, t := range r.tools {
		defs[i] = schema.ToolDef{
			Name: t.Name(), Description: t.Description(), Parameters: t.InputSchema(),
		}
	}
	return defs
}

func (r *registryImpl) FindByName(name string) (Tool, bool) {
	for _, t := range r.tools {
		if t.Name() == name {
			return t, true
		}
	}
	return nil, false
}

package llm

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"

	"github.com/cloudwego/eino/components/model"
)

const (
	OpenRouter = "openrouter"
	Ollama     = "ollama"
)

// Providers are the upstream endpoints a model can be served by.
var Providers = []string{OpenRouter, Ollama}

// DefaultBaseURL is where a provider is reached unless the backoffice or
// the environment says otherwise.
var DefaultBaseURL = map[string]string{
	OpenRouter: "https://openrouter.ai/api/v1",
	Ollama:     "http://localhost:11434/v1",
}

func IsProvider(id string) bool { return slices.Contains(Providers, id) }

// Provider is an upstream endpoint and its key.
type Provider struct {
	ID      string
	BaseURL string
	APIKey  string
}

// Dialer makes the chat model for a provider. NewChatModel is the real one.
type Dialer func(ctx context.Context, p Provider) (model.ToolCallingChatModel, error)

func DialOpenAI(ctx context.Context, p Provider) (model.ToolCallingChatModel, error) {
	return NewChatModel(ctx, p.ID, p.BaseURL, p.APIKey)
}

// Snapshot is a catalog together with the chat models that serve it.
type Snapshot struct {
	Catalog *Catalog
	models  map[string]model.ToolCallingChatModel
}

// ChatModel returns the chat model that serves m.
func (s *Snapshot) ChatModel(m Model) model.ToolCallingChatModel { return s.models[m.Provider] }

// Registry holds the live snapshot. The backoffice swaps it at run time;
// a request takes one snapshot and keeps it, so a change never mixes the
// old catalog with the new providers.
type Registry struct {
	dial Dialer
	cur  atomic.Pointer[Snapshot]
}

func NewRegistry(dial Dialer) *Registry { return &Registry{dial: dial} }

func (r *Registry) Current() *Snapshot { return r.cur.Load() }

func (r *Registry) Swap(s *Snapshot) { r.cur.Store(s) }

// Build checks that every provider the catalog uses is set up and dials it.
// Nothing is swapped in; the caller does that once the change is saved.
func (r *Registry) Build(ctx context.Context, c *Catalog, providers []Provider) (*Snapshot, error) {
	byID := map[string]Provider{}
	for _, p := range providers {
		byID[p.ID] = p
	}
	s := &Snapshot{Catalog: c, models: map[string]model.ToolCallingChatModel{}}
	for _, m := range c.Models {
		if _, done := s.models[m.Provider]; done {
			continue
		}
		p, ok := byID[m.Provider]
		switch {
		case !ok || p.BaseURL == "":
			return nil, fmt.Errorf("%s needs %s, which has no base URL", m.ID, m.Provider)
		case p.ID == OpenRouter && p.APIKey == "":
			return nil, fmt.Errorf("%s needs %s, which has no API key", m.ID, m.Provider)
		}
		cm, err := r.dial(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.ID, err)
		}
		s.models[p.ID] = cm
	}
	return s, nil
}

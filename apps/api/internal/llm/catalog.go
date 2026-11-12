package llm

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
)

// Model is a whitelisted model. Only catalog models can be used; for
// OpenRouter they are picked so they route to private (ZDR) providers.
//
// Prices use OpenRouter's unit, USD per million tokens, which is numerically
// the same as micro-USD per token. Local Ollama models are given prices too,
// so budgeting behaves exactly as it would against OpenRouter.
type Model struct {
	ID string `json:"id"`
	// Provider is the endpoint that serves the model: openrouter, ollama or
	// nearai.
	Provider string `json:"provider"`
	Upstream string `json:"upstream"`
	Name     string `json:"name"`
	// Description is the model's own blurb for the picker. It's written in
	// the backoffice, not taken from the provider.
	Description string `json:"description,omitempty"`
	// Identity is added to the system prompt, so a rebranded model
	// introduces itself as Name and Description rather than its upstream
	// name. Empty leaves the model as the provider made it.
	Identity             string  `json:"identity,omitempty"`
	PromptUSDPerMTok     float64 `json:"prompt_usd_per_mtok"`
	CompletionUSDPerMTok float64 `json:"completion_usd_per_mtok"`
	ContextTokens        int     `json:"context_tokens"`
	MaxOutputTokens      int     `json:"max_output_tokens"`
	// Vision models accept image attachments. ImageTokens is the per-image
	// prompt allowance used for holds; images are downscaled before sending
	// so the real count stays under it.
	Vision      bool `json:"vision"`
	ImageTokens int  `json:"image_tokens,omitempty"`
	// Tools models can call tools (calculator, search, python).
	Tools bool `json:"tools"`
	// Icon is the model's badge in the UI.
	Icon Icon `json:"icon"`
}

// Icon is a transit-style line badge: a short code in a colored shape.
type Icon struct {
	Code  string `json:"code"`
	Color string `json:"color"`
	// Shape is circle, square, diamond or hexagon.
	Shape string `json:"shape"`
}

// Cost in micro-USD, rounded up.
func (m Model) Cost(promptTokens, completionTokens int) int64 {
	c := float64(promptTokens)*m.PromptUSDPerMTok + float64(completionTokens)*m.CompletionUSDPerMTok
	return int64(math.Ceil(c))
}

// ChargeFor picks the amount to bill: the catalog price the user sees, or
// the upstream-reported cost if that is higher.
func (m Model) ChargeFor(u Usage) int64 {
	return max(u.CostMicros, m.Cost(u.PromptTokens, u.CompletionTokens))
}

type Catalog struct {
	Models []Model `json:"models"`
	byID   map[string]Model
	def    string
	single string
}

// LoadCatalog reads a catalog file. Models that don't name a provider get
// the given one.
func LoadCatalog(path, provider string) (*Catalog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("model catalog: %w", err)
	}
	var c Catalog
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("model catalog %s: %w", path, err)
	}
	for i := range c.Models {
		if c.Models[i].Provider == "" {
			c.Models[i].Provider = provider
		}
	}
	return NewCatalog(c.Models)
}

func NewCatalog(models []Model) (*Catalog, error) {
	c := &Catalog{Models: models, byID: make(map[string]Model, len(models))}
	for i := range models {
		models[i].Icon = models[i].Icon.withDefaults(models[i].ID, i)
	}
	for _, m := range models {
		switch {
		case m.ID == "" || m.Upstream == "":
			return nil, fmt.Errorf("model catalog: id and upstream are required")
		case !IsProvider(m.Provider):
			return nil, fmt.Errorf("model catalog: %s: provider must be one of %s", m.ID, strings.Join(Providers, ", "))
		case m.ContextTokens <= 0 || m.MaxOutputTokens <= 0 || m.MaxOutputTokens >= m.ContextTokens:
			return nil, fmt.Errorf("model catalog: %s: need 0 < max_output_tokens < context_tokens", m.ID)
		case m.PromptUSDPerMTok < 0 || m.CompletionUSDPerMTok < 0:
			return nil, fmt.Errorf("model catalog: %s: negative price", m.ID)
		case m.Vision && m.ImageTokens <= 0:
			return nil, fmt.Errorf("model catalog: %s: vision models need image_tokens", m.ID)
		}
		if _, dup := c.byID[m.ID]; dup {
			return nil, fmt.Errorf("model catalog: duplicate id %s", m.ID)
		}
		c.byID[m.ID] = m
	}
	if len(c.byID) == 0 {
		return nil, fmt.Errorf("model catalog: no models")
	}
	return c, nil
}

func (c *Catalog) Get(id string) (Model, bool) {
	m, ok := c.byID[id]
	return m, ok
}

// Default is the model picked by WithDefault, else the first one.
func (c *Catalog) Default() Model {
	if m, ok := c.byID[c.def]; ok {
		return m
	}
	return c.Models[0]
}

// WithDefault makes id the default model. An unknown id is ignored, so a
// disabled default falls back to the first model.
func (c *Catalog) WithDefault(id string) *Catalog {
	c.def = id
	return c
}

// Single is the only model offered when single model mode is on, or "" when
// it's off.
func (c *Catalog) Single() string { return c.single }

// WithSingle turns single model mode on for id. An unknown or empty id turns
// it off, so a disabled model doesn't leave the catalog stuck on it.
func (c *Catalog) WithSingle(id string) *Catalog {
	c.single = ""
	if _, ok := c.byID[id]; ok {
		c.single = id
	}
	return c
}

var iconColors = []string{"#1e5bff", "#ffc400", "#e8322b", "#7b4bd6", "#2fbf71"}

// withDefaults fills a missing badge from the model ID and its position.
func (ic Icon) withDefaults(id string, i int) Icon {
	if ic.Code == "" {
		code := []rune(strings.ToUpper(strings.NewReplacer("-", "", ".", "", ":", "", "/", "").Replace(id)))
		ic.Code = string(code[:min(2, len(code))])
	}
	if ic.Color == "" {
		ic.Color = iconColors[i%len(iconColors)]
	}
	switch ic.Shape {
	case "circle", "square", "diamond", "hexagon":
	default:
		ic.Shape = "circle"
	}
	return ic
}

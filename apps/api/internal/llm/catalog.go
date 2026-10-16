package llm

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// Model is a whitelisted model. Only catalog models can be used; for
// OpenRouter they are picked so they route to private (ZDR) providers.
//
// Prices use OpenRouter's unit, USD per million tokens, which is numerically
// the same as micro-USD per token. Local Ollama models are given prices too,
// so budgeting behaves exactly as it would against OpenRouter.
type Model struct {
	ID                   string  `json:"id"`
	Upstream             string  `json:"upstream"`
	Name                 string  `json:"name"`
	PromptUSDPerMTok     float64 `json:"prompt_usd_per_mtok"`
	CompletionUSDPerMTok float64 `json:"completion_usd_per_mtok"`
	ContextTokens        int     `json:"context_tokens"`
	MaxOutputTokens      int     `json:"max_output_tokens"`
	// Vision models accept image attachments. ImageTokens is the per-image
	// prompt allowance used for holds; images are downscaled before sending
	// so the real count stays under it.
	Vision      bool `json:"vision"`
	ImageTokens int  `json:"image_tokens,omitempty"`
}

// Cost in micro-USD, rounded up.
func (m Model) Cost(promptTokens, completionTokens int) int64 {
	c := float64(promptTokens)*m.PromptUSDPerMTok + float64(completionTokens)*m.CompletionUSDPerMTok
	return int64(math.Ceil(c))
}

// ChargeFor picks the amount to bill: the upstream-reported cost when there
// is one, otherwise the catalog price.
func (m Model) ChargeFor(u Usage) int64 {
	if u.CostMicros > 0 {
		return u.CostMicros
	}
	return m.Cost(u.PromptTokens, u.CompletionTokens)
}

type Catalog struct {
	Models []Model `json:"models"`
	byID   map[string]Model
}

func LoadCatalog(path string) (*Catalog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("model catalog: %w", err)
	}
	var c Catalog
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("model catalog %s: %w", path, err)
	}
	return NewCatalog(c.Models)
}

func NewCatalog(models []Model) (*Catalog, error) {
	c := &Catalog{Models: models, byID: make(map[string]Model, len(models))}
	for _, m := range models {
		switch {
		case m.ID == "" || m.Upstream == "":
			return nil, fmt.Errorf("model catalog: id and upstream are required")
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

func (c *Catalog) Default() Model { return c.Models[0] }

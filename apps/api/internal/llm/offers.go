package llm

import (
	"context"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Offer is a model a provider serves, as the backoffice shows it before it's
// added to the catalog. Prices are USD per million tokens like Model's.
type Offer struct {
	Upstream             string  `json:"upstream"`
	Name                 string  `json:"name"`
	Description          string  `json:"description,omitempty"`
	PromptUSDPerMTok     float64 `json:"prompt_usd_per_mtok"`
	CompletionUSDPerMTok float64 `json:"completion_usd_per_mtok"`
	ContextTokens        int     `json:"context_tokens,omitempty"`
	// MaxOutputTokens is the provider's limit, 0 when it doesn't say.
	MaxOutputTokens int  `json:"max_output_tokens,omitempty"`
	Vision          bool `json:"vision"`
	Tools           bool `json:"tools"`
	// ZDR is whether a zero-data-retention endpoint serves it. Every
	// OpenRouter request is restricted to those, so others won't answer.
	ZDR bool `json:"zdr"`
	// Verifiable is NEAR AI's flag for models served from a TEE that can be
	// attested. Its other models are proxied to their vendors.
	Verifiable bool `json:"verifiable"`
}

// Offers lists the models a provider serves. OpenRouter's come most used
// first, with details and ZDR endpoint prices; Ollama only names its models.
// Neither needs the key.
func Offers(ctx context.Context, p Provider) ([]Offer, error) {
	base := strings.TrimRight(p.BaseURL, "/")
	if p.ID == NearAI {
		return nearOffers(ctx, base, p.APIKey)
	}
	if p.ID != OpenRouter {
		var list struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := probeGet(ctx, base+"/models", p.APIKey, &list); err != nil {
			return nil, err
		}
		out := make([]Offer, 0, len(list.Data))
		for _, m := range list.Data {
			out = append(out, Offer{Upstream: m.ID, Name: m.ID, Tools: true, ZDR: true})
		}
		return out, nil
	}

	type price struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	}
	var models struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			Description   string `json:"description"`
			ContextLength int    `json:"context_length"`
			Architecture  struct {
				InputModalities []string `json:"input_modalities"`
			} `json:"architecture"`
			Pricing     price `json:"pricing"`
			TopProvider struct {
				MaxCompletionTokens int `json:"max_completion_tokens"`
			} `json:"top_provider"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"data"`
	}
	if err := probeGet(ctx, base+"/models?sort=top-weekly", p.APIKey, &models); err != nil {
		return nil, err
	}
	var zdr struct {
		Data []struct {
			ModelID string `json:"model_id"`
			Pricing price  `json:"pricing"`
		} `json:"data"`
	}
	if err := probeGet(ctx, base+"/endpoints/zdr", p.APIKey, &zdr); err != nil {
		return nil, err
	}
	// The cheapest ZDR endpoint is the suggested price. Users are billed the
	// reported cost when it's higher, so a pricier endpoint isn't a loss.
	cheapest := map[string][2]float64{}
	for _, e := range zdr.Data {
		in, out := perMTok(e.Pricing.Prompt), perMTok(e.Pricing.Completion)
		if in < 0 || out < 0 {
			continue
		}
		if c, ok := cheapest[e.ModelID]; !ok || in+out < c[0]+c[1] {
			cheapest[e.ModelID] = [2]float64{in, out}
		}
	}

	out := make([]Offer, 0, len(models.Data))
	for _, m := range models.Data {
		o := Offer{
			Upstream:             m.ID,
			Name:                 m.Name,
			Description:          summary(m.Description),
			PromptUSDPerMTok:     perMTok(m.Pricing.Prompt),
			CompletionUSDPerMTok: perMTok(m.Pricing.Completion),
			ContextTokens:        m.ContextLength,
			MaxOutputTokens:      m.TopProvider.MaxCompletionTokens,
		}
		// Routers like openrouter/auto price per request, as -1.
		if o.PromptUSDPerMTok < 0 || o.CompletionUSDPerMTok < 0 {
			continue
		}
		for _, in := range m.Architecture.InputModalities {
			o.Vision = o.Vision || in == "image"
		}
		for _, sp := range m.SupportedParameters {
			o.Tools = o.Tools || sp == "tools"
		}
		if c, ok := cheapest[m.ID]; ok {
			o.ZDR = true
			o.PromptUSDPerMTok, o.CompletionUSDPerMTok = c[0], c[1]
		}
		out = append(out, o)
	}
	return out, nil
}

// nearOffers lists NEAR AI Cloud's chat models from /model/list, which
// carries prices and the verifiable (TEE) flag. Private ones come first.
func nearOffers(ctx context.Context, base, key string) ([]Offer, error) {
	type amount struct {
		Amount int64 `json:"amount"`
		Scale  int   `json:"scale"`
	}
	var list struct {
		Models []struct {
			ModelID  string `json:"modelId"`
			Input    amount `json:"inputCostPerToken"`
			Output   amount `json:"outputCostPerToken"`
			Metadata struct {
				Verifiable    bool   `json:"verifiable"`
				ContextLength int    `json:"contextLength"`
				MaxOutput     int    `json:"maxOutputLength"`
				DisplayName   string `json:"modelDisplayName"`
				Description   string `json:"modelDescription"`
				Architecture  struct {
					Input  []string `json:"inputModalities"`
					Output []string `json:"outputModalities"`
				} `json:"architecture"`
				Features []string `json:"supportedFeatures"`
			} `json:"metadata"`
		} `json:"models"`
	}
	if err := probeGet(ctx, base+"/model/list", key, &list); err != nil {
		return nil, err
	}
	perM := func(a amount) float64 {
		return math.Round(float64(a.Amount)/math.Pow10(a.Scale)*1e12) / 1e6
	}
	out := make([]Offer, 0, len(list.Models))
	for _, m := range list.Models {
		md := m.Metadata
		// Chat models only: text in, text out (no embeddings, images, audio).
		if !slices.Contains(md.Architecture.Input, "text") || !slices.Equal(md.Architecture.Output, []string{"text"}) {
			continue
		}
		name := md.DisplayName
		if name == "" {
			name = m.ModelID
		}
		out = append(out, Offer{
			Upstream:             m.ModelID,
			Name:                 name,
			Description:          summary(md.Description),
			PromptUSDPerMTok:     perM(m.Input),
			CompletionUSDPerMTok: perM(m.Output),
			ContextTokens:        md.ContextLength,
			MaxOutputTokens:      md.MaxOutput,
			Vision:               slices.Contains(md.Architecture.Input, "image"),
			Tools:                slices.Contains(md.Features, "tools"),
			// No routing restriction is sent, so every model answers.
			ZDR:        true,
			Verifiable: md.Verifiable,
		})
	}
	slices.SortStableFunc(out, func(a, b Offer) int {
		switch {
		case a.Verifiable == b.Verifiable:
			return 0
		case a.Verifiable:
			return -1
		}
		return 1
	})
	return out, nil
}

// perMTok turns OpenRouter's USD-per-token string into USD per million
// tokens; unparsable prices read as -1.
func perMTok(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return -1
	}
	return math.Round(f*1e12) / 1e6
}

// summary keeps a description's first sentence or so.
func summary(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ". "); i > 0 && i < 240 {
		return s[:i+1]
	}
	if r := []rune(s); len(r) > 240 {
		return strings.TrimSpace(string(r[:240])) + "…"
	}
	return s
}

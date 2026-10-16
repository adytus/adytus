package llm

import (
	"encoding/json"
	"errors"

	"github.com/cloudwego/eino/schema"
)

// Token estimates run before a request, to size the credit hold and fit the
// history into the context window. They are deliberately pessimistic (about
// 3 bytes per token, where real tokenizers average ~4 for English) so holds
// cover the real cost. Billing always uses the provider's reported counts.
const (
	bytesPerToken    = 3
	perMessageTokens = 4
	perRequestTokens = 3
)

func EstimateTokens(s string) int {
	return (len(s) + bytesPerToken - 1) / bytesPerToken
}

func (m Model) messageTokens(msg *schema.Message) int {
	n := perMessageTokens + EstimateTokens(msg.Content)
	for _, p := range msg.UserInputMultiContent {
		switch p.Type {
		case schema.ChatMessagePartTypeText:
			n += EstimateTokens(p.Text)
		case schema.ChatMessagePartTypeImageURL:
			n += m.ImageTokens
		}
	}
	for _, tc := range msg.ToolCalls {
		n += EstimateTokens(tc.Function.Name) + EstimateTokens(tc.Function.Arguments)
	}
	return n
}

// EstimateMessages estimates the prompt tokens for msgs sent to m.
func (m Model) EstimateMessages(msgs []*schema.Message) int {
	n := perRequestTokens
	for _, msg := range msgs {
		n += m.messageTokens(msg)
	}
	return n
}

// EstimateTools estimates the prompt tokens taken by tool definitions.
func EstimateTools(tools []*schema.ToolInfo) int {
	n := 0
	for _, t := range tools {
		n += perMessageTokens + EstimateTokens(t.Name) + EstimateTokens(t.Desc)
		if s, err := t.ParamsOneOf.ToJSONSchema(); err == nil && s != nil {
			if raw, err := json.Marshal(s); err == nil {
				n += EstimateTokens(string(raw))
			}
		}
	}
	return n
}

var ErrPromptTooLong = errors.New("message is too long for this model's context window")

// FitContext keeps the system prompt (if first) and the newest messages that
// fit in the model's context alongside a full-length reply. Older messages are
// dropped first. It returns the kept messages and their token estimate.
func FitContext(m Model, msgs []*schema.Message) ([]*schema.Message, int, error) {
	budget := m.ContextTokens - m.MaxOutputTokens - perRequestTokens
	if len(msgs) == 0 {
		return nil, 0, errors.New("no messages")
	}

	var system []*schema.Message
	rest := msgs
	if msgs[0].Role == schema.System {
		system, rest = msgs[:1], msgs[1:]
		budget -= m.messageTokens(msgs[0])
	}

	kept := 0
	for i := len(rest) - 1; i >= 0; i-- {
		cost := m.messageTokens(rest[i])
		if cost > budget {
			break
		}
		budget -= cost
		kept++
	}
	if kept == 0 {
		return nil, 0, ErrPromptTooLong
	}

	out := append(append([]*schema.Message{}, system...), rest[len(rest)-kept:]...)
	return out, m.EstimateMessages(out), nil
}

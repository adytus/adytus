package llm

import "errors"

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

func EstimateMessages(msgs []Message) int {
	n := perRequestTokens
	for _, m := range msgs {
		n += perMessageTokens + EstimateTokens(m.Content)
	}
	return n
}

var ErrPromptTooLong = errors.New("message is too long for this model's context window")

// FitContext keeps the system prompt (if first) and the newest messages that
// fit in the model's context alongside a full-length reply. Older messages are
// dropped first. It returns the kept messages and their token estimate.
func FitContext(m Model, msgs []Message) ([]Message, int, error) {
	budget := m.ContextTokens - m.MaxOutputTokens - perRequestTokens
	if len(msgs) == 0 {
		return nil, 0, errors.New("no messages")
	}

	var system []Message
	rest := msgs
	if msgs[0].Role == "system" {
		system, rest = msgs[:1], msgs[1:]
		budget -= perMessageTokens + EstimateTokens(msgs[0].Content)
	}

	kept := 0
	for i := len(rest) - 1; i >= 0; i-- {
		cost := perMessageTokens + EstimateTokens(rest[i].Content)
		if cost > budget {
			break
		}
		budget -= cost
		kept++
	}
	if kept == 0 {
		return nil, 0, ErrPromptTooLong
	}

	out := append(append([]Message{}, system...), rest[len(rest)-kept:]...)
	return out, EstimateMessages(out), nil
}

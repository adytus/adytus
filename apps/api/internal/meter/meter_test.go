package meter

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"github.com/reaganiwadha/adytus/apps/api/internal/billing"
	"github.com/reaganiwadha/adytus/apps/api/internal/llm"
)

var spec = llm.Model{
	ID: "m", Upstream: "up", PromptUSDPerMTok: 1, CompletionUSDPerMTok: 2,
	ContextTokens: 10000, MaxOutputTokens: 100,
}

type fakeLedger struct {
	mu       sync.Mutex
	credit   int64 // reserve fails once holds would exceed it
	held     map[uuid.UUID]int64
	settled  []billing.Usage
	released int
}

func newLedger(credit int64) *fakeLedger {
	return &fakeLedger{credit: credit, held: map[uuid.UUID]int64{}}
}

func (l *fakeLedger) Reserve(_ context.Context, _ uuid.UUID, amount int64) (uuid.UUID, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if amount > l.credit {
		return uuid.Nil, billing.ErrNoCredit
	}
	l.credit -= amount
	id := uuid.New()
	l.held[id] = amount
	return id, nil
}

func (l *fakeLedger) Settle(_ context.Context, _, holdID uuid.UUID, u billing.Usage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.credit += l.held[holdID] - u.CostMicros
	delete(l.held, holdID)
	l.settled = append(l.settled, u)
	return nil
}

func (l *fakeLedger) Release(_ context.Context, holdID uuid.UUID) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.credit += l.held[holdID]
	delete(l.held, holdID)
	l.released++
	return nil
}

// scripted replies with its turns in order and records the options it got.
type scripted struct {
	mu    sync.Mutex
	turns []*schema.Message
	opts  []*model.Options
	tools []*schema.ToolInfo
}

func (s *scripted) next(opts []model.Option) *schema.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts = append(s.opts, model.GetCommonOptions(nil, opts...))
	m := s.turns[0]
	s.turns = s.turns[1:]
	return m
}

func (s *scripted) Generate(_ context.Context, _ []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return s.next(opts), nil
}

func (s *scripted) Stream(_ context.Context, _ []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m := s.next(opts)
	// Content first, then a usage-only chunk, as OpenAI-style streams do.
	body := *m
	body.ResponseMeta = nil
	return schema.StreamReaderFromArray([]*schema.Message{&body, {Role: schema.Assistant, ResponseMeta: m.ResponseMeta}}), nil
}

func (s *scripted) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	s.tools = tools
	return s, nil
}

func reply(content string, prompt, completion int, calls ...schema.ToolCall) *schema.Message {
	return &schema.Message{
		Role: schema.Assistant, Content: content, ToolCalls: calls,
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: prompt, CompletionTokens: completion}},
	}
}

func drain(t *testing.T, sr *schema.StreamReader[*schema.Message]) string {
	t.Helper()
	defer sr.Close()
	var out string
	for {
		m, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out += m.Content
	}
}

func TestStreamSettlesBeforeEOF(t *testing.T) {
	ledger := newLedger(1_000_000)
	inner := &scripted{turns: []*schema.Message{reply("hi", 100, 10)}}
	m := New(inner, ledger, uuid.New(), spec)

	msgs := []*schema.Message{schema.UserMessage("hello")}
	if _, err := m.Reserve(context.Background(), msgs); err != nil {
		t.Fatal(err)
	}
	sr, err := m.Stream(context.Background(), msgs, model.WithModel("evil"), model.WithMaxTokens(99999))
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(t, sr); got != "hi" {
		t.Fatalf("content %q", got)
	}
	// No Wait: settling happens before EOF.
	if u := m.Usage(); u.Calls != 1 || u.PromptTokens != 100 || u.CompletionTokens != 10 || u.CostMicros != 120 {
		t.Fatalf("usage %+v", u)
	}
	if len(ledger.held) != 0 || len(ledger.settled) != 1 {
		t.Fatalf("holds left %d, settled %d", len(ledger.held), len(ledger.settled))
	}
	o := inner.opts[0]
	if *o.Model != "up" || *o.MaxTokens != 100 {
		t.Fatalf("catalog not pinned: model %s, max %d", *o.Model, *o.MaxTokens)
	}
}

func TestStreamClosedEarlyBillsEstimate(t *testing.T) {
	ledger := newLedger(1_000_000)
	inner := &scripted{turns: []*schema.Message{{Role: schema.Assistant, Content: "partial answer"}}}
	m := New(inner, ledger, uuid.New(), spec)
	sr, err := m.Stream(context.Background(), []*schema.Message{schema.UserMessage("hello")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sr.Recv(); err != nil {
		t.Fatal(err)
	}
	sr.Close()
	m.Wait()
	if u := m.Usage(); u.Calls != 1 || u.CompletionTokens != llm.EstimateTokens("partial answer") {
		t.Fatalf("usage %+v", u)
	}
}

func TestNothingGeneratedReleases(t *testing.T) {
	ledger := newLedger(1_000_000)
	inner := &scripted{turns: []*schema.Message{{Role: schema.Assistant}}}
	m := New(inner, ledger, uuid.New(), spec)
	if _, err := m.Generate(context.Background(), []*schema.Message{schema.UserMessage("x")}); err != nil {
		t.Fatal(err)
	}
	if ledger.released != 1 || m.Usage().Calls != 0 || ledger.credit != 1_000_000 {
		t.Fatalf("released %d, usage %+v, credit %d", ledger.released, m.Usage(), ledger.credit)
	}
}

// An Eino ReAct agent makes one model call per step; each one is held and
// settled separately, so a tool loop pays as it goes.
func TestAgentToolLoopIsMeteredPerCall(t *testing.T) {
	ctx := context.Background()
	ledger := newLedger(1_000_000)
	inner := &scripted{turns: []*schema.Message{
		reply("", 200, 20, schema.ToolCall{ID: "c1", Type: "function", Function: schema.FunctionCall{Name: "lookup", Arguments: `{"q":"paris"}`}}),
		reply("Paris has 2.1M people.", 260, 12),
	}}
	m := New(inner, ledger, uuid.New(), spec)

	type args struct {
		Q string `json:"q" jsonschema:"description=what to look up"`
	}
	lookup, err := utils.InferTool("lookup", "Look up a fact", func(_ context.Context, a args) (string, error) {
		return "population of " + a.Q + ": 2.1M", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: m,
		ToolsConfig:      compose.ToolsNodeConfig{Tools: []tool.BaseTool{lookup}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := agent.Generate(ctx, []*schema.Message{schema.UserMessage("How many people live in Paris?")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "Paris has 2.1M people." {
		t.Fatalf("answer %q", out.Content)
	}
	u := m.Usage()
	if u.Calls != 2 || u.PromptTokens != 460 || u.CompletionTokens != 32 {
		t.Fatalf("usage %+v", u)
	}
	if len(ledger.settled) != 2 || len(ledger.held) != 0 {
		t.Fatalf("settled %d, holds left %d", len(ledger.settled), len(ledger.held))
	}
	if len(inner.tools) != 1 || inner.tools[0].Name != "lookup" {
		t.Fatalf("tools not bound: %+v", inner.tools)
	}
}

// A loop stops at the step that can't be covered, not after it.
func TestAgentStopsWhenCreditRunsOut(t *testing.T) {
	ctx := context.Background()
	msgs := []*schema.Message{schema.UserMessage("go")}
	_, oneHold := New(nil, nil, uuid.Nil, spec).holdFor(msgs)
	ledger := newLedger(oneHold + 50) // enough for the first step only
	inner := &scripted{turns: []*schema.Message{
		reply("", 10, 10, schema.ToolCall{ID: "c1", Type: "function", Function: schema.FunctionCall{Name: "noop", Arguments: `{}`}}),
	}}
	m := New(inner, ledger, uuid.New(), spec)
	noop, _ := utils.InferTool("noop", "Does nothing", func(context.Context, struct{}) (string, error) {
		return "ok", nil
	})
	agent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: m,
		ToolsConfig:      compose.ToolsNodeConfig{Tools: []tool.BaseTool{noop}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = agent.Generate(ctx, msgs)
	if !errors.Is(err, billing.ErrNoCredit) {
		t.Fatalf("expected ErrNoCredit, got %v", err)
	}
	if m.Usage().Calls != 1 || len(ledger.held) != 0 {
		t.Fatalf("usage %+v, holds left %d", m.Usage(), len(ledger.held))
	}
}

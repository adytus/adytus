// Package meter bills every model call against the user's credit. A Model
// wraps an Eino chat model: each Generate or Stream call reserves a hold,
// pins the catalog's upstream model and output limit, and settles what the
// call actually used. Agents and tool loops built on Eino make several calls
// per user message; each is metered on its own, so a loop stops as soon as
// credit or the daily cap runs out.
package meter

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"github.com/reaganiwadha/adytus/apps/api/internal/billing"
	"github.com/reaganiwadha/adytus/apps/api/internal/llm"
)

type Ledger interface {
	Reserve(ctx context.Context, userID uuid.UUID, amount int64) (uuid.UUID, error)
	Settle(ctx context.Context, userID, holdID uuid.UUID, u billing.Usage) error
	Release(ctx context.Context, holdID uuid.UUID) error
}

// Totals sums the calls a Model (and its WithTools copies) has settled.
type Totals struct {
	PromptTokens     int
	CompletionTokens int
	CostMicros       int64
	Calls            int
}

type state struct {
	mu      sync.Mutex
	pending uuid.NullUUID
	totals  Totals
	wg      sync.WaitGroup
}

type Model struct {
	inner      model.ToolCallingChatModel
	ledger     Ledger
	userID     uuid.UUID
	spec       llm.Model
	toolTokens int
	st         *state
}

var _ model.ToolCallingChatModel = (*Model)(nil)

func New(inner model.ToolCallingChatModel, ledger Ledger, userID uuid.UUID, spec llm.Model) *Model {
	return &Model{inner: inner, ledger: ledger, userID: userID, spec: spec, st: &state{}}
}

func (m *Model) holdFor(msgs []*schema.Message) (est int, amount int64) {
	est = m.spec.EstimateMessages(msgs) + m.toolTokens
	return est, m.spec.Cost(est, m.spec.OutputLimit(est))
}

// Reserve takes the hold for the first call up front, so a request without
// enough credit can be refused before a response starts. It returns the
// amount held. The next Generate or Stream uses this hold.
func (m *Model) Reserve(ctx context.Context, msgs []*schema.Message) (int64, error) {
	_, amount := m.holdFor(msgs)
	id, err := m.ledger.Reserve(ctx, m.userID, amount)
	if err != nil {
		return 0, err
	}
	m.st.mu.Lock()
	prev := m.st.pending
	m.st.pending = uuid.NullUUID{UUID: id, Valid: true}
	m.st.mu.Unlock()
	if prev.Valid {
		m.release(ctx, prev.UUID)
	}
	return amount, nil
}

// ReleasePending returns a hold taken by Reserve that no call used.
func (m *Model) ReleasePending(ctx context.Context) {
	m.st.mu.Lock()
	p := m.st.pending
	m.st.pending = uuid.NullUUID{}
	m.st.mu.Unlock()
	if p.Valid {
		m.release(ctx, p.UUID)
	}
}

// Wait blocks until every stream has been settled. Streams settle before
// their reader sees EOF, but a reader closed early settles in the background.
func (m *Model) Wait() { m.st.wg.Wait() }

func (m *Model) Usage() Totals {
	m.st.mu.Lock()
	defer m.st.mu.Unlock()
	return m.st.totals
}

func (m *Model) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	inner, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	cp := *m
	cp.inner = inner
	cp.toolTokens = llm.EstimateTools(tools)
	return &cp, nil
}

// hold returns the pending hold from Reserve, or reserves a new one.
func (m *Model) hold(ctx context.Context, msgs []*schema.Message) (uuid.UUID, int, error) {
	est, amount := m.holdFor(msgs)
	m.st.mu.Lock()
	p := m.st.pending
	m.st.pending = uuid.NullUUID{}
	m.st.mu.Unlock()
	if p.Valid {
		return p.UUID, est, nil
	}
	id, err := m.ledger.Reserve(ctx, m.userID, amount)
	return id, est, err
}

// pin appends after the caller's options so the catalog wins: users can
// only reach whitelisted models, with the output limit the hold covers.
func (m *Model) pin(est int, opts []model.Option) []model.Option {
	return append(opts, model.WithModel(m.spec.Upstream), model.WithMaxTokens(m.spec.OutputLimit(est)))
}

func (m *Model) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	in = llm.TrimToolResults(m.spec, in, m.toolTokens)
	holdID, est, err := m.hold(ctx, in)
	if err != nil {
		return nil, err
	}
	cctx, sink := llm.WithCostSink(ctx)
	out, err := m.inner.Generate(cctx, in, m.pin(est, opts)...)
	var c collected
	if out != nil {
		c.add(out)
	}
	m.settle(ctx, holdID, est, &c, sink)
	return out, err
}

func (m *Model) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	in = llm.TrimToolResults(m.spec, in, m.toolTokens)
	holdID, est, err := m.hold(ctx, in)
	if err != nil {
		return nil, err
	}
	cctx, sink := llm.WithCostSink(ctx)
	sr, err := m.inner.Stream(cctx, in, m.pin(est, opts)...)
	if err != nil {
		m.release(ctx, holdID)
		return nil, err
	}

	out, sw := schema.Pipe[*schema.Message](1)
	m.st.wg.Add(1)
	go func() {
		defer m.st.wg.Done()
		defer sr.Close()
		var c collected
		var streamErr error
		for {
			msg, err := sr.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				streamErr = err
				break
			}
			c.add(msg)
			if closed := sw.Send(msg, nil); closed {
				break
			}
		}
		// Settle before the reader sees the end, so Usage is final by then.
		m.settle(ctx, holdID, est, &c, sink)
		if streamErr != nil {
			sw.Send(nil, streamErr)
		}
		sw.Close()
	}()
	return out, nil
}

// collected is what a call produced, for billing when the provider reports
// no usage (a cancelled stream, or a provider that omits it).
type collected struct {
	output strings.Builder
	usage  *schema.TokenUsage
}

func (c *collected) add(msg *schema.Message) {
	if msg == nil {
		return
	}
	c.output.WriteString(msg.ReasoningContent)
	c.output.WriteString(msg.Content)
	for _, tc := range msg.ToolCalls {
		c.output.WriteString(tc.Function.Name)
		c.output.WriteString(tc.Function.Arguments)
	}
	if msg.ResponseMeta != nil && msg.ResponseMeta.Usage != nil {
		c.usage = msg.ResponseMeta.Usage
	}
}

func (m *Model) settle(ctx context.Context, holdID uuid.UUID, est int, c *collected, sink *llm.CostSink) {
	ctx = context.WithoutCancel(ctx)
	u := llm.Usage{CostMicros: sink.Micros()}
	switch {
	case c.usage != nil:
		u.PromptTokens, u.CompletionTokens = c.usage.PromptTokens, c.usage.CompletionTokens
	case c.output.Len() > 0:
		// Cut off before the usage chunk: bill the pessimistic estimate.
		u.PromptTokens, u.CompletionTokens = est, llm.EstimateTokens(c.output.String())
	default:
		// Nothing generated and nothing reported: the request never ran.
		m.release(ctx, holdID)
		return
	}
	cost := m.spec.ChargeFor(u)
	if err := m.ledger.Settle(ctx, m.userID, holdID, billing.Usage{
		Model: m.spec.ID, PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, CostMicros: cost,
	}); err != nil {
		slog.Error("settle usage", "err", err, "user", m.userID)
	}
	m.st.mu.Lock()
	t := &m.st.totals
	t.PromptTokens += u.PromptTokens
	t.CompletionTokens += u.CompletionTokens
	t.CostMicros += cost
	t.Calls++
	m.st.mu.Unlock()
}

func (m *Model) release(ctx context.Context, holdID uuid.UUID) {
	if err := m.ledger.Release(context.WithoutCancel(ctx), holdID); err != nil {
		slog.Error("release hold", "err", err)
	}
}

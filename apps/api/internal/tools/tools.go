// Package tools holds the tools a model may call: a calculator, web search
// and a Python sandbox. Each is an Eino tool, so Eino's agents drive the
// calling loop; this package only adds the user-facing descriptions and a
// hook that reports each call to the chat stream as it happens.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

// Info describes a tool to the user, for settings.
type Info struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// LeavesServer is true when using the tool sends data to third parties.
	LeavesServer bool `json:"leaves_server"`
	// DefaultOn applies until the user changes the setting.
	DefaultOn bool `json:"default_on"`
}

type entry struct {
	Info
	tool tool.InvokableTool
}

// Registry holds the tools this server is configured with.
type Registry struct {
	entries []entry
}

const (
	Calculator = "calculator"
	WebSearch  = "web_search"
	PythonID   = "python"
)

// NewRegistry builds the tools that are configured; search and python are
// left out when their backend is nil.
func NewRegistry(search *Search, py *Python) (*Registry, error) {
	r := &Registry{}

	calc, err := utils.InferTool(Calculator,
		"Evaluate an arithmetic expression exactly. Supports + - * / % ^, parentheses, pi, e and functions such as sqrt, pow, ln, log10, sin, cos, round. Use it for any arithmetic instead of computing in your head.",
		func(_ context.Context, in struct {
			Expression string `json:"expression" jsonschema:"description=The expression, e.g. (1234 * 5678) / 3"`
		}) (string, error) {
			out, err := Calculate(in.Expression)
			if err != nil {
				return "error: " + err.Error(), nil
			}
			return out, nil
		})
	if err != nil {
		return nil, err
	}
	r.entries = append(r.entries, entry{Info{
		ID: Calculator, Name: "Calculator",
		Description: "Exact arithmetic, worked out on this server.",
		DefaultOn:   true,
	}, calc})

	if search != nil {
		t, err := utils.InferTool(WebSearch,
			"Search the web. Returns titles, URLs and snippets of the top results. Use it for recent events or facts you are unsure of, and cite the URLs you rely on.",
			func(ctx context.Context, in struct {
				Query string `json:"query" jsonschema:"description=What to search for"`
			}) (string, error) {
				res, err := search.Query(ctx, in.Query, 5)
				if err != nil {
					return "error: " + err.Error(), nil
				}
				if len(res) == 0 {
					return "no results", nil
				}
				out, _ := json.Marshal(res)
				return string(out), nil
			})
		if err != nil {
			return nil, err
		}
		r.entries = append(r.entries, entry{Info{
			ID: WebSearch, Name: "Web search",
			Description:  "Looks things up on the web. The search text is sent to public search engines, without your account details.",
			LeavesServer: true,
		}, t})
	}

	if py != nil {
		t, err := utils.InferTool(PythonID,
			"Run Python 3.12 code and get its printed output. Standard library only, no network or files; each run starts fresh. print() whatever you need to see. Use it for data processing, simulations and precise calculations.",
			func(ctx context.Context, in struct {
				Code string `json:"code" jsonschema:"description=A complete Python program; print the results"`
			}) (string, error) {
				res, err := py.Run(ctx, in.Code)
				if err != nil {
					return "error: " + err.Error(), nil
				}
				var sb strings.Builder
				sb.WriteString(res.Stdout)
				if res.Stderr != "" {
					sb.WriteString("\n[stderr]\n" + res.Stderr)
				}
				if res.TimedOut {
					sb.WriteString("\n[stopped: time limit reached]")
				} else if res.ExitCode != 0 {
					fmt.Fprintf(&sb, "\n[exit code %d]", res.ExitCode)
				}
				if sb.Len() == 0 {
					return "(no output; print() the result)", nil
				}
				return sb.String(), nil
			})
		if err != nil {
			return nil, err
		}
		r.entries = append(r.entries, entry{Info{
			ID: PythonID, Name: "Python sandbox",
			Description: "Runs code the model writes, isolated on this server: no internet, no files, a few seconds per run.",
			DefaultOn:   true,
		}, t})
	}
	return r, nil
}

func (r *Registry) Infos() []Info {
	out := make([]Info, len(r.entries))
	for i, e := range r.entries {
		out[i] = e.Info
	}
	return out
}

// Enabled resolves the user's choices (tool ID -> on/off) against defaults.
func (r *Registry) Enabled(choices map[string]bool) []string {
	var ids []string
	for _, e := range r.entries {
		on, set := choices[e.ID]
		if (set && on) || (!set && e.DefaultOn) {
			ids = append(ids, e.ID)
		}
	}
	return ids
}

func (r *Registry) Has(id string) bool {
	for _, e := range r.entries {
		if e.ID == id {
			return true
		}
	}
	return false
}

// Step is one tool call, as shown to the user and kept with the reply.
type Step struct {
	ID     string `json:"id"`
	Tool   string `json:"tool"`
	Input  string `json:"input"`
	Output string `json:"output,omitempty"`
	Done   bool   `json:"done"`
	Millis int64  `json:"ms,omitempty"`
}

// Observer is told about each call as it starts and finishes.
type Observer func(Step)

// Tools returns the given tools wrapped to report calls to observe.
func (r *Registry) Tools(ids []string, observe Observer) []tool.BaseTool {
	var out []tool.BaseTool
	for _, e := range r.entries {
		for _, id := range ids {
			if e.ID == id {
				out = append(out, &observed{e.tool, observe})
			}
		}
	}
	return out
}

const maxStepOutput = 4000

type observed struct {
	tool.InvokableTool
	observe Observer
}

func (o *observed) InvokableRun(ctx context.Context, args string, opts ...tool.Option) (string, error) {
	info, err := o.Info(ctx)
	if err != nil {
		return "", err
	}
	step := Step{ID: fmt.Sprintf("%d", time.Now().UnixNano()), Tool: info.Name, Input: args}
	if o.observe != nil {
		o.observe(step)
	}
	start := time.Now()
	out, err := o.InvokableTool.InvokableRun(ctx, args, opts...)
	if err != nil {
		// Errors go back to the model as text so it can recover.
		out, err = "error: "+err.Error(), nil
	}
	step.Output, step.Done, step.Millis = truncate(out, maxStepOutput), true, time.Since(start).Milliseconds()
	if o.observe != nil {
		o.observe(step)
	}
	return truncate(out, maxStepOutput), err
}

var _ tool.InvokableTool = (*observed)(nil)

// Schema for tests and for estimating prompt size.
func (r *Registry) ToolInfos(ctx context.Context, ids []string) ([]*schema.ToolInfo, error) {
	var out []*schema.ToolInfo
	for _, t := range r.Tools(ids, nil) {
		info, err := t.Info(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

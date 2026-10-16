package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// Python runs code in CPython compiled to WebAssembly (WASI), inside the API
// process. The guest has no network (WASI preview 1 has no sockets), no host
// filesystem, no environment, and bounded memory and time. Each run starts
// from a fresh instance, so nothing carries over between calls or users.
type Python struct {
	rt       wazero.Runtime
	mod      wazero.CompiledModule
	timeout  time.Duration
	maxOut   int
	slots    chan struct{}
	initOnce sync.Once
}

// PythonConfig bounds a sandbox run.
type PythonConfig struct {
	WasmPath string
	Timeout  time.Duration // wall clock per run
	MemoryMB uint32        // guest linear memory cap
	MaxOut   int           // bytes of stdout/stderr returned to the model
	Parallel int           // concurrent runs
}

// NewPython compiles the interpreter once; compilation takes a few seconds,
// runs are then fast to start.
func NewPython(ctx context.Context, cfg PythonConfig) (*Python, error) {
	wasm, err := os.ReadFile(cfg.WasmPath)
	if err != nil {
		return nil, fmt.Errorf("python sandbox: %w", err)
	}
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithMemoryLimitPages(cfg.MemoryMB*16). // 64 KiB pages
		WithCloseOnContextDone(true))
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	mod, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("python sandbox: compile: %w", err)
	}
	return &Python{rt: rt, mod: mod, timeout: cfg.Timeout, maxOut: cfg.MaxOut, slots: make(chan struct{}, max(1, cfg.Parallel))}, nil
}

func (p *Python) Close(ctx context.Context) error { return p.rt.Close(ctx) }

// limitedBuffer keeps the first n bytes and notes that more was written.
type limitedBuffer struct {
	buf       bytes.Buffer
	n         int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.n - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	if b.buf.Len() >= b.n && len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	if b.truncated {
		return b.buf.String() + "\n[output truncated]"
	}
	return b.buf.String()
}

type PythonResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr,omitempty"`
	ExitCode uint32 `json:"exit_code"`
	TimedOut bool   `json:"timed_out,omitempty"`
}

var ErrSandboxBusy = errors.New("the python sandbox is busy; try again shortly")

func (p *Python) Run(ctx context.Context, code string) (PythonResult, error) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-ctx.Done():
		return PythonResult{}, ctx.Err()
	case <-time.After(5 * time.Second):
		return PythonResult{}, ErrSandboxBusy
	}

	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	stdout := &limitedBuffer{n: p.maxOut}
	stderr := &limitedBuffer{n: p.maxOut}
	cfg := wazero.NewModuleConfig().
		WithName("").
		WithArgs("python", "-I", "-S", "-c", code).
		WithStdout(stdout).
		WithStderr(stderr).
		WithStdin(strings.NewReader("")).
		WithRandSource(cryptoRand{}).
		WithSysWalltime().
		WithSysNanotime()

	mod, err := p.rt.InstantiateModule(ctx, p.mod, cfg)
	if mod != nil {
		mod.Close(context.Background())
	}
	res := PythonResult{Stdout: stdout.String(), Stderr: stderr.String()}
	var exit *sys.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		res.ExitCode = exit.ExitCode()
		if exit.ExitCode() == sys.ExitCodeDeadlineExceeded || exit.ExitCode() == sys.ExitCodeContextCanceled {
			res.TimedOut = true
		}
	case ctx.Err() != nil:
		res.TimedOut = true
	default:
		// Traps (e.g. the memory cap) surface as plain errors.
		res.ExitCode = 1
		res.Stderr += "\n" + err.Error()
	}
	return res, nil
}

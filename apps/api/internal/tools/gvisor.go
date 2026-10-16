package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Gvisor runs Python in containers under gVisor (runsc), through the docker
// CLI. Containers have no network, a read-only root, no capabilities and an
// unprivileged user; only small tmpfs mounts under /work and at /tmp are
// writable. Input files are copied into /work/in before the code runs and
// whatever it leaves in /work/out is copied back, both as tar streams.
// Each container serves one run and is then removed, so nothing carries over
// between calls or users. A few are kept started ahead of time, so a run only
// pays for `docker exec`.
type Gvisor struct {
	cfg    GvisorConfig
	slots  chan struct{}
	idle   chan string
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// GvisorConfig bounds a sandbox run.
type GvisorConfig struct {
	Image    string        // built from deploy/sandbox
	Runtime  string        // "runsc"; "runc" gives up gVisor's isolation
	Timeout  time.Duration // wall clock per run
	MemoryMB int
	CPUs     string // docker --cpus, e.g. "1"
	MaxOut   int    // bytes of stdout/stderr returned to the model
	Parallel int    // concurrent runs
	Warm     int    // containers kept started ahead of runs
}

type PythonResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr,omitempty"`
	ExitCode uint32 `json:"exit_code"`
	TimedOut bool   `json:"timed_out,omitempty"`
	// Files are what the code left in OutDir.
	Files []File `json:"-"`
	// Note says which output files were dropped, if any.
	Note string `json:"-"`
}

var ErrSandboxBusy = errors.New("the python sandbox is busy; try again shortly")

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

// containerLabel marks sandbox containers, so ones left behind by a crash
// are removed on the next start.
const containerLabel = "adytus.sandbox=python"

// NewGvisor checks that docker has the runtime and the image, clears old
// sandbox containers and starts the warm pool.
func NewGvisor(ctx context.Context, cfg GvisorConfig) (*Gvisor, error) {
	out, err := docker(ctx, "info", "--format", "{{json .Runtimes}}")
	if err != nil {
		return nil, fmt.Errorf("python sandbox: docker: %w", err)
	}
	if cfg.Runtime == "" {
		cfg.Runtime = "runsc"
	}
	if !strings.Contains(out, strconv.Quote(cfg.Runtime)) {
		return nil, fmt.Errorf("python sandbox: docker has no %q runtime; install gVisor (https://gvisor.dev/docs/user_guide/install/), "+
			"then run: sudo runsc install && sudo systemctl restart docker", cfg.Runtime)
	}
	if cfg.Runtime != "runsc" {
		slog.Warn("python sandbox is not using gVisor; model-written code runs on the host kernel", "runtime", cfg.Runtime)
	}
	if _, err := docker(ctx, "image", "inspect", "--format", "{{.Id}}", cfg.Image); err != nil {
		return nil, fmt.Errorf("python sandbox: image %q not found; build it with: docker build -t %s deploy/sandbox", cfg.Image, cfg.Image)
	}
	removeLabelled(ctx)

	g := &Gvisor{
		cfg:   cfg,
		slots: make(chan struct{}, max(1, cfg.Parallel)),
		idle:  make(chan string, max(0, cfg.Warm)),
	}
	if cfg.Warm > 0 {
		fillCtx, cancel := context.WithCancel(context.Background())
		g.cancel = cancel
		g.wg.Add(1)
		go g.fill(fillCtx)
	}
	return g, nil
}

func (g *Gvisor) About() string {
	return "numpy, scipy, Pillow and matplotlib (Agg backend) are installed; nothing else can be installed."
}

// Close stops the pool and removes every sandbox container.
func (g *Gvisor) Close(ctx context.Context) error {
	if g.cancel != nil {
		g.cancel()
		g.wg.Wait()
	}
	removeLabelled(ctx)
	return nil
}

// fill keeps the idle pool topped up; the channel's capacity is its size.
func (g *Gvisor) fill(ctx context.Context) {
	defer g.wg.Done()
	for {
		id, err := g.start(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("python sandbox: start container", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		select {
		case g.idle <- id:
		case <-ctx.Done():
			remove(id)
			return
		}
	}
}

// start creates a container that waits for one exec.
func (g *Gvisor) start(ctx context.Context) (string, error) {
	mem := strconv.Itoa(g.cfg.MemoryMB) + "m"
	out, err := docker(ctx, "run", "-d", "--rm",
		"--label", containerLabel,
		"--runtime", g.cfg.Runtime,
		"--network", "none",
		"--read-only",
		"--tmpfs", "/work:rw,size=16m,mode=1777",
		"--tmpfs", InDir+":rw,size=40m,mode=1777",
		"--tmpfs", OutDir+":rw,size=40m,mode=1777",
		"--tmpfs", "/tmp:rw,size=64m,mode=1777",
		"--user", "65534:65534",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--memory", mem, "--memory-swap", mem,
		"--cpus", g.cfg.CPUs,
		"--pids-limit", "64",
		"--log-driver", "none",
		g.cfg.Image, "sleep", "infinity")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (g *Gvisor) Run(ctx context.Context, code string, in []File) (PythonResult, error) {
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	case <-ctx.Done():
		return PythonResult{}, ctx.Err()
	case <-time.After(5 * time.Second):
		return PythonResult{}, ErrSandboxBusy
	}

	var id string
	select {
	case id = <-g.idle:
	default:
		var err error
		if id, err = g.start(ctx); err != nil {
			return PythonResult{}, fmt.Errorf("start sandbox: %w", err)
		}
	}
	defer remove(id)

	if len(in) > 0 {
		if err := g.copyIn(ctx, id, in); err != nil {
			return PythonResult{}, fmt.Errorf("copy files in: %w", err)
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, g.cfg.Timeout)
	defer cancel()
	stdout := &limitedBuffer{n: g.cfg.MaxOut}
	stderr := &limitedBuffer{n: g.cfg.MaxOut}
	// The code goes in on stdin rather than argv: no length limit, and it
	// stays out of process listings.
	cmd := exec.CommandContext(runCtx, "docker", "exec", "-i", id, "python", "-I", "-")
	cmd.Stdin = strings.NewReader(code)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()

	res := PythonResult{Stdout: stdout.String(), Stderr: stderr.String()}
	var exit *exec.ExitError
	switch {
	case runCtx.Err() != nil:
		res.TimedOut = true
	case err == nil:
	case errors.As(err, &exit):
		res.ExitCode = uint32(exit.ExitCode())
		if exit.ExitCode() == 137 {
			res.Stderr += "\n[killed: memory limit reached]"
		}
	default:
		return PythonResult{}, err
	}
	if !res.TimedOut {
		var out outputs
		if err := g.copyOut(ctx, id, &out); err != nil {
			slog.Warn("python sandbox: copy files out", "err", err)
			res.Note = "\n[output files could not be read]"
		} else {
			res.Files, res.Note = out.files, out.note()
		}
	}
	return res, nil
}

// copyIn extracts files into InDir.
func (g *Gvisor) copyIn(ctx context.Context, id string, in []File) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeTar(pw, in)) }()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", id, "tar", "-x", "-C", InDir)
	cmd.Stdin = pr
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// copyOut reads OutDir as a tar stream. The container is untrusted, so
// the stream is only parsed, within the outputs' limits.
func (g *Gvisor) copyOut(ctx context.Context, id string, out *outputs) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "exec", id, "tar", "-c", "-C", OutDir, ".")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	readErr := out.readTar(io.LimitReader(pipe, 64<<20))
	_, _ = io.Copy(io.Discard, pipe)
	if err := cmd.Wait(); err != nil {
		return err
	}
	return readErr
}

// remove deletes a container without waiting on the caller's context, which
// may already be done.
func remove(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := docker(ctx, "rm", "-f", id); err != nil {
		slog.Warn("python sandbox: remove container", "id", id, "err", err)
	}
}

func removeLabelled(ctx context.Context) {
	out, err := docker(ctx, "ps", "-aq", "--filter", "label="+containerLabel)
	if err != nil || strings.TrimSpace(out) == "" {
		return
	}
	_, _ = docker(ctx, append([]string{"rm", "-f"}, strings.Fields(out)...)...)
}

func docker(ctx context.Context, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return stdout.String(), nil
}

package tools

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Pyodide runs Python in Pyodide (CPython built for WebAssembly) under Deno,
// one process per run. Deno is started with no permissions but reading the
// Pyodide directory (and the uid, which a package asks for), so code has no
// network, environment, subprocesses or host files, and its memory is
// WebAssembly's. The process starts with an empty environment. Input files
// are written to InDir in Pyodide's in-memory filesystem and whatever the
// code leaves in OutDir comes back, both over stdin and stdout. Nothing
// carries over between calls or users. A few processes are booted ahead of
// time, so a run only pays for its imports.
type Pyodide struct {
	cfg    PyodideConfig
	deno   string
	dir    string // absolute
	tmp    string // the runner script and Deno's cache
	runner string
	about  string
	slots  chan struct{}
	idle   chan *proc
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// PyodideConfig bounds a sandbox run.
type PyodideConfig struct {
	Dir      string        // made by deploy/pyodide/fetch.ts
	Timeout  time.Duration // wall clock per run, imports included
	MemoryMB int           // resident memory of the whole process
	MaxOut   int           // bytes of stdout/stderr returned to the model
	Parallel int           // concurrent runs
	Warm     int           // processes kept booted ahead of runs
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

//go:embed pyodide_runner.mjs
var runnerScript []byte

// bootTimeout bounds starting a process, which loads Pyodide.
const bootTimeout = time.Minute

// maxResult bounds the result line: the output files in base64, and output.
const maxResult = maxOutputsTotal*4/3 + 4<<20

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

// NewPyodide checks that Deno and the Pyodide directory are there and starts
// the warm pool.
func NewPyodide(ctx context.Context, cfg PyodideConfig) (*Pyodide, error) {
	deno, err := exec.LookPath("deno")
	if err != nil {
		return nil, errors.New("python sandbox: deno not found on PATH; install it from https://deno.com")
	}
	dir, err := filepath.Abs(cfg.Dir)
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Version string   `json:"version"`
		Wheels  []string `json:"wheels"`
		Fonts   []string `json:"fonts"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "adytus.json"))
	if err == nil {
		err = json.Unmarshal(data, &manifest)
	}
	if err != nil {
		return nil, fmt.Errorf("python sandbox: no Pyodide in %s; fetch it from the repo root with:\n"+
			"  deno run --allow-net --allow-read --allow-write deploy/pyodide/fetch.ts %s", dir, cfg.Dir)
	}
	tmp, err := os.MkdirTemp("", "adytus-pyodide-")
	if err != nil {
		return nil, err
	}
	runner := filepath.Join(tmp, "runner.mjs")
	if err := os.WriteFile(runner, runnerScript, 0o444); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	p := &Pyodide{
		cfg: cfg, deno: deno, dir: dir, tmp: tmp, runner: runner,
		slots: make(chan struct{}, max(1, cfg.Parallel)),
		idle:  make(chan *proc, max(0, cfg.Warm)),
		about: aboutPackages,
	}
	if len(manifest.Fonts) > 0 {
		p.about += aboutFonts
	}
	// Check it boots before taking requests.
	first, err := p.start()
	if err != nil {
		os.RemoveAll(tmp)
		return nil, fmt.Errorf("python sandbox: %w", err)
	}
	slog.Info("python sandbox ready", "pyodide", manifest.Version, "packages", len(manifest.Wheels), "fonts", len(manifest.Fonts))
	if cfg.Warm == 0 {
		first.kill()
	} else {
		p.idle <- first
		fillCtx, cancel := context.WithCancel(context.Background())
		p.cancel = cancel
		p.wg.Add(1)
		go p.fill(fillCtx)
	}
	return p, nil
}

const aboutPackages = "Packages: numpy, scipy, pandas, matplotlib (Agg), Pillow, sympy, lxml, beautifulsoup4, pyyaml, xlrd; " +
	"for documents python-docx, python-pptx, openpyxl, xlsxwriter, pypdf, pdfminer.six, fpdf2 and tabulate. Nothing else can be installed. " +
	"Big packages take a few seconds to import, so import only what you use."

// aboutFonts describes the fonts deploy/pyodide/fetch.ts adds, which the
// runner mounts at /opt/fonts.
const aboutFonts = " Fonts for Japanese, Chinese and Korean text (Latin too) are in /opt/fonts: NotoSansJP.ttf, NotoSansSC.ttf (Simplified), NotoSansTC.ttf (Traditional) and NotoSansKR.ttf, " +
	"variable fonts whose default weight is regular. Built-in PDF fonts can't draw these scripts, so to write a PDF with them, " +
	"don't edit content streams: build it with fpdf2, e.g. pdf.add_font(\"JP\", \"\", \"/opt/fonts/NotoSansJP.ttf\"), " +
	"and for bold pdf.add_font(\"JP\", \"B\", \"/opt/fonts/NotoSansJP.ttf\", variations={\"wght\": 700}) (a few seconds), " +
	"then copy the original's layout, or draw new text onto pages of the original with pypdf's merge_page " +
	"(a white box over old text hides it but leaves it in the file; rebuild the page when it must be gone). " +
	"For matplotlib, matplotlib.font_manager.fontManager.addfont(path) and then set rcParams[\"font.family\"] to \"Noto Sans JP\" (or SC, TC, KR)."

func (p *Pyodide) About() string { return p.about }

// Close stops the pool and kills idle processes.
func (p *Pyodide) Close(context.Context) error {
	if p.cancel != nil {
		p.cancel()
		p.wg.Wait()
	}
	for {
		select {
		case pr := <-p.idle:
			pr.kill()
		default:
			return os.RemoveAll(p.tmp)
		}
	}
}

// fill keeps the idle pool topped up; the channel's capacity is its size.
func (p *Pyodide) fill(ctx context.Context) {
	defer p.wg.Done()
	for {
		pr, err := p.start()
		if err != nil {
			slog.Error("python sandbox: start", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		select {
		case p.idle <- pr:
		case <-ctx.Done():
			pr.kill()
			return
		}
	}
}

// proc is a booted runner waiting for its one job.
type proc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *limitedBuffer // Deno's own errors; read only after done
	done   chan struct{}  // closed once the process has exited
}

func (pr *proc) kill() {
	_ = pr.cmd.Process.Kill()
	<-pr.done
}

func (pr *proc) exited() bool {
	select {
	case <-pr.done:
		return true
	default:
		return false
	}
}

// start boots a runner and waits for it to say it is ready.
func (p *Pyodide) start() (*proc, error) {
	cmd := exec.Command(p.deno, "run",
		"--quiet", "--no-prompt", "--no-config", "--no-lock", "--no-remote", "--no-npm",
		"--allow-read="+p.dir+","+p.runner,
		"--allow-sys=uid",
		// WebAssembly memory isn't on the V8 heap; this bounds the JS side.
		"--v8-flags=--max-old-space-size=256",
		p.runner, p.dir)
	cmd.Env = []string{
		"DENO_DIR=" + filepath.Join(p.tmp, "deno"),
		"DENO_NO_UPDATE_CHECK=1",
		"NO_COLOR=1",
	}
	cmd.Dir = p.tmp
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	pr := &proc{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(io.LimitReader(stdout, maxResult)),
		stderr: &limitedBuffer{n: 8 << 10}, done: make(chan struct{})}
	cmd.Stderr = pr.stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		_ = cmd.Wait()
		close(pr.done)
	}()

	ready := make(chan error, 1)
	go func() {
		line, err := pr.stdout.ReadString('\n')
		if err == nil && strings.TrimSpace(line) != `{"ready":true}` {
			err = fmt.Errorf("unexpected %q", line)
		}
		ready <- err
	}()
	select {
	case err = <-ready:
	case <-time.After(bootTimeout):
		err = errors.New("timed out")
	}
	if err != nil {
		pr.kill()
		return nil, fmt.Errorf("boot pyodide: %w: %s", err, strings.TrimSpace(pr.stderr.String()))
	}
	return pr, nil
}

type pyJob struct {
	Code          string   `json:"code"`
	Files         []pyFile `json:"files"`
	MaxFiles      int      `json:"max_files"`
	MaxFileBytes  int      `json:"max_file_bytes"`
	MaxTotalBytes int      `json:"max_total_bytes"`
}

type pyFile struct {
	Name string `json:"name"`
	Size int64  `json:"size,omitempty"`
	Data []byte `json:"data,omitempty"` // base64 in JSON
}

type pyResult struct {
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
	ExitCode int      `json:"exit_code"`
	Files    []pyFile `json:"files"`
}

func (p *Pyodide) Run(ctx context.Context, code string, in []File) (PythonResult, error) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-ctx.Done():
		return PythonResult{}, ctx.Err()
	case <-time.After(5 * time.Second):
		return PythonResult{}, ErrSandboxBusy
	}

	var pr *proc
	select {
	case pr = <-p.idle:
	default:
	}
	if pr == nil || pr.exited() {
		var err error
		if pr, err = p.start(); err != nil {
			return PythonResult{}, fmt.Errorf("start sandbox: %w", err)
		}
	}
	defer pr.kill()

	job := pyJob{Code: code, MaxFiles: MaxOutputFiles, MaxFileBytes: MaxOutputBytes, MaxTotalBytes: maxOutputsTotal}
	for _, f := range in {
		job.Files = append(job.Files, pyFile{Name: f.Name, Data: f.Data})
	}
	body, err := json.Marshal(job)
	if err != nil {
		return PythonResult{}, err
	}

	runCtx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	// The process could stop reading, so stdin is written apart.
	go func() {
		_, _ = pr.stdin.Write(body)
		_ = pr.stdin.Close()
	}()
	type read struct {
		line []byte
		err  error
	}
	got := make(chan read, 1)
	go func() {
		line, err := io.ReadAll(pr.stdout)
		got <- read{line, err}
	}()
	overMemory := make(chan struct{})
	go p.watchMemory(runCtx, pr, overMemory)

	var r read
	res := PythonResult{}
	select {
	case r = <-got:
	case <-overMemory:
		return p.memoryKilled(pr), nil
	case <-runCtx.Done():
		pr.kill()
		if ctx.Err() != nil {
			return PythonResult{}, ctx.Err()
		}
		res.TimedOut = true
		return res, nil
	}
	select {
	case <-pr.done:
	case <-time.After(5 * time.Second):
	}
	pr.kill()
	// A run that went over between two checks still went over.
	if peakRSS(pr.cmd.ProcessState) > int64(p.cfg.MemoryMB)<<20 {
		return p.memoryKilled(pr), nil
	}

	var out pyResult
	if r.err != nil || json.Unmarshal(r.line, &out) != nil {
		msg := strings.TrimSpace(pr.stderr.String())
		slog.Warn("python sandbox: run failed", "err", r.err, "stderr", msg)
		res.Stderr = "[the sandbox stopped unexpectedly]"
		if i := strings.LastIndex(msg, "Error:"); i >= 0 {
			res.Stderr += "\n" + msg[i:]
		}
		res.ExitCode = 1
		return res, nil
	}
	stdout, stderr := &limitedBuffer{n: p.cfg.MaxOut}, &limitedBuffer{n: p.cfg.MaxOut}
	stdout.Write([]byte(out.Stdout))
	stderr.Write([]byte(out.Stderr))
	res.Stdout, res.Stderr, res.ExitCode = stdout.String(), stderr.String(), uint32(out.ExitCode)

	var files outputs
	for _, f := range out.Files {
		if int64(len(f.Data)) != f.Size {
			// Left out by the runner for being over the limits; add notes why.
			f.Data = nil
		}
		if err := files.add(f.Name, f.Size, bytes.NewReader(f.Data)); err != nil {
			return PythonResult{}, err
		}
	}
	res.Files, res.Note = files.files, files.note()
	return res, nil
}

func (p *Pyodide) memoryKilled(pr *proc) PythonResult {
	pr.kill()
	return PythonResult{
		Stderr:   fmt.Sprintf("[killed: memory limit (%d MB) reached]", p.cfg.MemoryMB),
		ExitCode: 137,
	}
}

// watchMemory closes over when the process's resident memory passes the
// limit. It reads /proc, so it only works on Linux; elsewhere the peak is
// still checked after the run.
func (p *Pyodide) watchMemory(ctx context.Context, pr *proc, over chan<- struct{}) {
	if runtime.GOOS != "linux" {
		return
	}
	limit := int64(p.cfg.MemoryMB) << 20
	path := "/proc/" + strconv.Itoa(pr.cmd.Process.Pid) + "/statm"
	page := int64(os.Getpagesize())
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-pr.done:
			return
		case <-t.C:
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		fields := strings.Fields(string(data))
		if len(fields) < 2 {
			return
		}
		pages, _ := strconv.ParseInt(fields[1], 10, 64)
		if pages*page > limit {
			close(over)
			return
		}
	}
}

// peakRSS is the most resident memory an exited process used, in bytes.
func peakRSS(ps *os.ProcessState) int64 {
	if ps == nil {
		return 0
	}
	ru, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return ru.Maxrss // bytes there, KB on Linux
	}
	return ru.Maxrss << 10
}

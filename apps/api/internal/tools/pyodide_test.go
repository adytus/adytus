package tools

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// newPyodide needs deno and apps/api/.pyodide (or PYODIDE_DIR), from
// deploy/pyodide/fetch.ts.
func newPyodide(t *testing.T, timeout time.Duration) *Pyodide {
	t.Helper()
	dir := os.Getenv("PYODIDE_DIR")
	if dir == "" {
		dir = "../../.pyodide"
	}
	p, err := NewPyodide(context.Background(), PyodideConfig{
		Dir: dir, Timeout: timeout, MemoryMB: 700, MaxOut: 2000, Parallel: 2, Warm: 1,
	})
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { p.Close(context.Background()) })
	return p
}

func TestPyodideRuns(t *testing.T) {
	p := newPyodide(t, 60*time.Second)
	res, err := p.Run(context.Background(), `
import numpy as np, scipy.linalg, PIL.Image, matplotlib.pyplot as plt
plt.plot([1, 2]); plt.savefig("/work/fig.png")
print(int(np.linalg.det(scipy.linalg.hilbert(2) * 12)), PIL.Image.open("/work/fig.png").format)
`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != "12 PNG" {
		t.Fatalf("%+v", res)
	}

	res, _ = p.Run(context.Background(), "import sys\nprint('bye')\nsys.exit(3)", nil)
	if res.ExitCode != 3 || res.Stdout != "bye\n" {
		t.Fatalf("exit: %+v", res)
	}
	res, _ = p.Run(context.Background(), "def f():\n    raise ValueError('boom')\nf()", nil)
	if res.ExitCode != 1 || !strings.Contains(res.Stderr, "ValueError: boom") || strings.Contains(res.Stderr, "_adytus_run") {
		t.Fatalf("traceback: %+v", res)
	}
}

func TestPyodideIsolated(t *testing.T) {
	t.Setenv("SERVER_KEY", "must-not-leak")
	p := newPyodide(t, 30*time.Second)
	cases := map[string]string{
		"socket": "import socket\ns = socket.create_connection(('1.1.1.1', 80), timeout=3)\nprint('connected')",
		// Async calls can't be awaited here, so ask Deno what it would allow.
		"perms":  "import js\nfrom pyodide.ffi import to_js\nfor n in ['net', 'env', 'write', 'run', 'ffi']:\n    if js.Deno.permissions.querySync(to_js({'name': n}, dict_converter=js.Object.fromEntries)).state == 'granted': print('connected', n)",
		"env":    "import os\nprint(os.environ.get('SERVER_KEY', ''))",
		"jsenv":  "import js\nprint(js.Deno.env.get('SERVER_KEY'))",
		"hostfs": "import js\nprint('connected', js.Deno.readTextFileSync('/etc/passwd'))",
		"mount":  "import os, js, pyodide_js\nfrom pyodide.ffi import to_js\nos.makedirs('/mnt/h')\npyodide_js.FS.mount(pyodide_js.FS.filesystems.NODEFS, to_js({'root': '/etc'}, dict_converter=js.Object.fromEntries), '/mnt/h')\nprint('connected', os.listdir('/mnt/h'))",
		"pkgs":   "open('/opt/site-packages/evil.py', 'w').write('x')\nprint('connected')",
		"run":    "import js\njs.Deno.Command.new('/bin/sh').outputSync()\nprint('connected')",
		"carry1": "open('/work/x', 'w').write('left')",
		"carry2": "import os\nprint('connected' if os.path.exists('/work/x') else '')",
	}
	for _, name := range []string{"socket", "perms", "env", "jsenv", "hostfs", "mount", "pkgs", "run", "carry1", "carry2"} {
		res, err := p.Run(context.Background(), cases[name], nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(res.Stdout, "connected") || strings.Contains(res.Stdout, "must-not-leak") {
			t.Errorf("%s escaped: %+v", name, res)
		}
	}
}

func TestPyodideLimits(t *testing.T) {
	p := newPyodide(t, 5*time.Second)
	res, err := p.Run(context.Background(), "while True: pass", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("expected timeout: %+v", res)
	}
	res, _ = p.Run(context.Background(), "x = b'x' * (1024 * 1024 * 1024)\nprint('allocated')", nil)
	if strings.Contains(res.Stdout, "allocated") || res.ExitCode == 0 {
		t.Fatalf("memory cap not enforced: %+v", res)
	}
	res, _ = p.Run(context.Background(), "print('x' * 10000)", nil)
	if !strings.HasSuffix(res.Stdout, "[output truncated]") {
		t.Fatalf("output not truncated: %d bytes", len(res.Stdout))
	}
}

func TestPyodideFiles(t *testing.T) {
	p := newPyodide(t, 30*time.Second)
	res, err := p.Run(context.Background(), `
from PIL import Image
im = Image.open("/work/in/photo.png")
im.resize((im.width // 2, im.height // 2)).save("/work/out/small.png")
open("/work/out/notes.txt", "w").write(open("/work/in/notes.txt").read().upper())
import os
os.symlink("/work/in/notes.txt", "/work/out/link")
os.makedirs("/work/out/sub")
open("/work/out/sub/nested.txt", "w").write("x")
print(im.size)
`, []File{{"photo.png", testPNG(t, 40, 20)}, {"notes.txt", []byte("hello")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != "(40, 20)" {
		t.Fatalf("%+v", res)
	}
	got := map[string][]byte{}
	for _, f := range res.Files {
		got[f.Name] = f.Data
	}
	if len(got) != 2 || string(got["notes.txt"]) != "HELLO" {
		t.Fatalf("outputs: %v", slices.Collect(maps.Keys(got)))
	}
	im, err := png.Decode(bytes.NewReader(got["small.png"]))
	if err != nil || im.Bounds().Dx() != 20 {
		t.Fatalf("resized image: %v %v", err, im)
	}

	// Too many files: the first ones are kept and the rest noted.
	res, _ = p.Run(context.Background(), "for i in range(12): open(f'/work/out/{i:02}.txt', 'w').write('x')", nil)
	if len(res.Files) != MaxOutputFiles || !strings.Contains(res.Note, "too many") || res.Files[0].Name != "00.txt" {
		t.Fatalf("limits: %d files, note %q", len(res.Files), res.Note)
	}
	// Too large: noted, not sent.
	res, _ = p.Run(context.Background(), "open('/work/out/big.bin', 'wb').write(b'x' * (21 << 20))", nil)
	if len(res.Files) != 0 || !strings.Contains(res.Note, "big.bin (too large)") {
		t.Fatalf("large file: %d files, note %q", len(res.Files), res.Note)
	}
}

// TestPyodideDocuments edits each kind of document the tool advertises.
func TestPyodideDocuments(t *testing.T) {
	p := newPyodide(t, 60*time.Second)
	res, err := p.Run(context.Background(), `
import docx, openpyxl, pptx, pypdf, fpdf, io
from pdfminer.high_level import extract_text

d = docx.Document(); d.add_heading("Report", 1); d.add_paragraph("hello docx"); d.save("/work/out/r.docx")
print(docx.Document("/work/out/r.docx").paragraphs[1].text)

wb = openpyxl.Workbook(); wb.active["A1"] = 41; wb.active["A2"] = "=A1+1"; wb.save("/work/out/s.xlsx")
print(openpyxl.load_workbook("/work/out/s.xlsx").active["A1"].value)

pr = pptx.Presentation(); s = pr.slides.add_slide(pr.slide_layouts[1]); s.shapes.title.text = "Slide one"; pr.save("/work/out/p.pptx")
print(pptx.Presentation("/work/out/p.pptx").slides[0].shapes.title.text)

pdf = fpdf.FPDF(); pdf.add_page(); pdf.set_font("helvetica", size=12); pdf.cell(text="hello pdf"); pdf.output("/work/out/d.pdf")
print(pypdf.PdfReader("/work/out/d.pdf").pages[0].extract_text().strip(), extract_text("/work/out/d.pdf").strip())
`, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "hello docx\n41\nSlide one\nhello pdf hello pdf\n"
	if res.ExitCode != 0 || res.Stdout != want || len(res.Files) != 4 {
		t.Fatalf("%+v", res)
	}
}

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

package tools

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// newGvisor needs docker, the adytus-sandbox image and the runsc runtime.
// PYTHON_RUNTIME=runc runs the tests without gVisor, for machines without it.
func newGvisor(t *testing.T, timeout time.Duration) *Gvisor {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found")
	}
	g, err := NewGvisor(context.Background(), GvisorConfig{
		Image: "adytus-sandbox", Runtime: os.Getenv("PYTHON_RUNTIME"), Timeout: timeout,
		MemoryMB: 128, CPUs: "1", MaxOut: 2000, Parallel: 2, Warm: 1,
	})
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { g.Close(context.Background()) })
	return g
}

func TestGvisorRuns(t *testing.T) {
	g := newGvisor(t, 30*time.Second)
	res, err := g.Run(context.Background(), `
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
}

func TestGvisorIsolated(t *testing.T) {
	t.Setenv("SERVER_KEY", "must-not-leak")
	g := newGvisor(t, 30*time.Second)
	cases := map[string]string{
		"socket": "import socket\ns = socket.create_connection(('1.1.1.1', 80), timeout=3)\nprint('connected')",
		"dns":    "import socket\nsocket.getaddrinfo('example.com', 80)\nprint('connected')",
		"rootfs": "open('/usr/evil', 'w')\nprint('connected')",
		"env":    "import os\nprint(os.environ.get('SERVER_KEY', ''))",
		"user":   "import os\nprint('connected' if os.getuid() == 0 else 'nobody')",
		"carry1": "open('/work/x', 'w').write('left')",
		"carry2": "import os\nprint('connected' if os.path.exists('/work/x') else '')",
	}
	for _, name := range []string{"socket", "dns", "rootfs", "env", "user", "carry1", "carry2"} {
		res, err := g.Run(context.Background(), cases[name], nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(res.Stdout, "connected") || strings.Contains(res.Stdout, "must-not-leak") {
			t.Errorf("%s escaped: %+v", name, res)
		}
	}
}

func TestGvisorLimits(t *testing.T) {
	g := newGvisor(t, 3*time.Second)
	res, err := g.Run(context.Background(), "while True: pass", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("expected timeout: %+v", res)
	}
	res, _ = g.Run(context.Background(), "x = b'x' * (512 * 1024 * 1024)\nprint('allocated')", nil)
	if strings.Contains(res.Stdout, "allocated") || res.ExitCode == 0 {
		t.Fatalf("memory cap not enforced: %+v", res)
	}
	res, _ = g.Run(context.Background(), "print('x' * 10000)", nil)
	if !strings.HasSuffix(res.Stdout, "[output truncated]") {
		t.Fatalf("output not truncated: %d bytes", len(res.Stdout))
	}
}

func TestGvisorFiles(t *testing.T) {
	g := newGvisor(t, 30*time.Second)
	res, err := g.Run(context.Background(), `
from PIL import Image
im = Image.open("/work/in/photo.png")
im.resize((im.width // 2, im.height // 2)).save("/work/out/small.png")
open("/work/out/notes.txt", "w").write(open("/work/in/notes.txt").read().upper())
import os
os.symlink("/etc/passwd", "/work/out/passwd")
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
	res, _ = g.Run(context.Background(), "for i in range(12): open(f'/work/out/{i:02}.txt', 'w').write('x')", nil)
	if len(res.Files) != MaxOutputFiles || !strings.Contains(res.Note, "too many") {
		t.Fatalf("limits: %d files, note %q", len(res.Files), res.Note)
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

package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func newPython(t *testing.T, timeout time.Duration) *Python {
	t.Helper()
	path := os.Getenv("PYTHON_WASM")
	if path == "" {
		path = "../../third_party/python/python-3.12.0.wasm"
	}
	if _, err := os.Stat(path); err != nil {
		t.Skip("python wasm not found; run scripts/fetch-python-wasm.sh")
	}
	p, err := NewPython(context.Background(), PythonConfig{WasmPath: path, Timeout: timeout, MemoryMB: 128, MaxOut: 2000, Parallel: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close(context.Background()) })
	return p
}

func TestPythonRuns(t *testing.T) {
	p := newPython(t, 20*time.Second)
	res, err := p.Run(context.Background(), "import math, json, statistics\nprint(json.dumps({'r': math.sqrt(2), 'm': statistics.mean([1,2,3])}))")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, `"m": 2`) {
		t.Fatalf("%+v", res)
	}
}

func TestPythonIsolated(t *testing.T) {
	p := newPython(t, 20*time.Second)
	cases := map[string]string{
		"socket": "import socket\ns = socket.socket()\ns.connect(('1.1.1.1', 80))\nprint('connected')",
		"files":  "print(open('/etc/passwd').read())",
		"env":    "import os\nprint(os.environ.get('SERVER_KEY', ''), os.environ.get('HOME', ''))\nprint('connected' if os.environ else '')",
	}
	for name, code := range cases {
		res, err := p.Run(context.Background(), code)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(res.Stdout, "connected") || strings.Contains(res.Stdout, "root:") {
			t.Errorf("%s escaped: %+v", name, res)
		}
	}
}

func TestPythonLimits(t *testing.T) {
	p := newPython(t, 3*time.Second)
	res, err := p.Run(context.Background(), "while True: pass")
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("expected timeout: %+v", res)
	}
	res, _ = p.Run(context.Background(), "x = bytearray(512 * 1024 * 1024)\nprint('allocated')")
	if strings.Contains(res.Stdout, "allocated") || res.ExitCode == 0 {
		t.Fatalf("memory cap not enforced: %+v", res)
	}
	res, _ = p.Run(context.Background(), "print('x' * 10000)")
	if !strings.HasSuffix(res.Stdout, "[output truncated]") {
		t.Fatalf("output not truncated: %d bytes", len(res.Stdout))
	}
}

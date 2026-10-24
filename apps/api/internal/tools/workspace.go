package tools

import (
	"context"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
)

// File is a file going into or coming out of a sandbox run.
type File struct {
	Name string
	Data []byte
}

// Saved is a file a tool saved to the user's files, as shown on its step.
type Saved struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Image bool   `json:"image,omitempty"`
	Size  int64  `json:"size"`
}

// Workspace is the user's files a request's tools may read and write. The
// server provides one per reply; without one, tools see no files.
type Workspace interface {
	// Names lists the files that can be read.
	Names() []string
	Read(ctx context.Context, name string) ([]byte, error)
	// Save stores a new file for the user.
	Save(ctx context.Context, name string, data []byte) (Saved, error)
}

type workspaceKey struct{}

// WithWorkspace gives the tools called under ctx access to ws.
func WithWorkspace(ctx context.Context, ws Workspace) context.Context {
	return context.WithValue(ctx, workspaceKey{}, ws)
}

func workspaceFrom(ctx context.Context) Workspace {
	ws, _ := ctx.Value(workspaceKey{}).(Workspace)
	return ws
}

// Limits on what a sandbox run can take in and hand back.
const (
	MaxInputBytes   = 32 << 20
	MaxOutputFiles  = 8
	MaxOutputBytes  = 20 << 20 // per file
	maxOutputsTotal = 32 << 20
)

// Where code finds its inputs and leaves its outputs.
const (
	InDir  = "/work/in"
	OutDir = "/work/out"
)

// outputs collects a run's output files within the limits; files past them
// are noted and dropped.
type outputs struct {
	files   []File
	total   int
	skipped []string
}

func (o *outputs) add(name string, size int64, r io.Reader) error {
	name = path.Base(filepath.ToSlash(name))
	switch {
	case name == "." || name == "/" || strings.HasPrefix(name, "."):
		return nil
	case len(o.files) >= MaxOutputFiles:
		o.skipped = append(o.skipped, name+" (too many files)")
		return nil
	case size > MaxOutputBytes || o.total+int(size) > maxOutputsTotal:
		o.skipped = append(o.skipped, name+" (too large)")
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(r, size))
	if err != nil {
		return err
	}
	o.total += len(data)
	o.files = append(o.files, File{name, data})
	return nil
}

func (o *outputs) note() string {
	if len(o.skipped) == 0 {
		return ""
	}
	return fmt.Sprintf("\n[not saved: %s; at most %d files of %d MB each]", strings.Join(o.skipped, ", "), MaxOutputFiles, MaxOutputBytes>>20)
}

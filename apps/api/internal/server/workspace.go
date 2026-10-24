package server

import (
	"context"
	"fmt"
	"mime"
	"path"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/reaganiwadha/adytus/apps/api/internal/crypto"
	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
	"github.com/reaganiwadha/adytus/apps/api/internal/store"
	"github.com/reaganiwadha/adytus/apps/api/internal/tools"
	"github.com/reaganiwadha/adytus/apps/api/internal/vault"
)

const (
	// maxWorkspaceFiles caps how many of a chat's files the tools can see.
	maxWorkspaceFiles = 50
	// saveSlots is how many files the tools can save during one reply.
	saveSlots = 16
)

// workspace gives one reply's tools the chat's files. The reply streams
// after the request's master key is gone, so the keys it needs are
// unwrapped (to read) or made (to save) up front, and zeroed by close.
type workspace struct {
	s      *Server
	userID uuid.UUID

	mu    sync.Mutex
	files map[string]wsFile // by unique name
	order []string
	slots []wsFile // fresh objects for files the tools save
}

type wsFile struct {
	obj *store.Object
	dek []byte
}

// newWorkspace unwraps the keys of the given files (newest first, so the
// newest of two same-named files keeps the plain name) and makes the
// objects saved files will use.
func (s *Server) newWorkspace(ctx context.Context, a *authCtx, ids []uuid.UUID) (*workspace, error) {
	ws := &workspace{s: s, userID: a.UserID, files: map[string]wsFile{}}
	ok := false
	defer func() {
		if !ok {
			ws.close()
		}
	}()
	seen := map[uuid.UUID]bool{}
	for _, id := range ids {
		if seen[id] || len(ws.order) >= maxWorkspaceFiles {
			continue
		}
		seen[id] = true
		o, err := s.Store.GetObject(ctx, a.UserID, id, store.KindFile)
		if errs.IsNotFound(err) {
			continue // deleted since
		}
		if err != nil {
			return nil, err
		}
		dek, err := vault.DEK(a.MK, o)
		if err != nil {
			return nil, err
		}
		var meta fileMeta
		if err := vault.OpenWithDEK(dek, o, &meta); err != nil {
			crypto.Zero(dek)
			return nil, err
		}
		ws.add(meta.Name, wsFile{o, dek})
	}
	for range saveSlots {
		o, dek, err := vault.New(a.MK, nil, a.UserID, store.KindFile, uuid.NullUUID{})
		if err != nil {
			return nil, err
		}
		ws.slots = append(ws.slots, wsFile{o, dek})
	}
	ok = true
	return ws, nil
}

// add files f under name, or name-2, name-3… when name is taken.
func (ws *workspace) add(name string, f wsFile) string {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	unique := name
	for i := 2; ws.files[unique].obj != nil; i++ {
		unique = fmt.Sprintf("%s-%d%s", stem, i, ext)
	}
	ws.files[unique] = f
	ws.order = append(ws.order, unique)
	return unique
}

func (ws *workspace) Names() []string {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return append([]string(nil), ws.order...)
}

func (ws *workspace) Read(ctx context.Context, name string) ([]byte, error) {
	ws.mu.Lock()
	f, ok := ws.files[name]
	ws.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no such file")
	}
	sealed, err := ws.s.Blobs.Get(ctx, f.obj.S3Key.ValueOrZero())
	if err != nil {
		return nil, fmt.Errorf("storage unavailable")
	}
	return vault.OpenBytes(f.dek, f.obj.ID, sealed)
}

func (ws *workspace) Save(ctx context.Context, name string, data []byte) (tools.Saved, error) {
	ws.mu.Lock()
	if len(ws.slots) == 0 {
		ws.mu.Unlock()
		return tools.Saved{}, fmt.Errorf("at most %d files can be saved per reply", saveSlots)
	}
	f := ws.slots[0]
	ws.slots = ws.slots[1:]
	ws.mu.Unlock()

	meta, err := ws.s.storeFile(ctx, f.obj, f.dek, name, mime.TypeByExtension(path.Ext(name)), data)
	if err != nil {
		crypto.Zero(f.dek)
		return tools.Saved{}, fmt.Errorf("could not be stored")
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	// Later calls in the same reply can read it back.
	ws.add(meta.Name, f)
	return tools.Saved{ID: f.obj.ID.String(), Name: meta.Name, Image: meta.Image, Size: meta.Size}, nil
}

// prompt tells the model which files its tools can open.
func (ws *workspace) prompt() string {
	names := ws.Names()
	if len(names) == 0 {
		return ""
	}
	return "\n\nFiles in this chat, which your tools can open by name: " + strings.Join(names, ", ") + "."
}

func (ws *workspace) close() {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	for _, f := range ws.files {
		crypto.Zero(f.dek)
	}
	for _, f := range ws.slots {
		crypto.Zero(f.dek)
	}
	ws.slots = nil
}

package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Call is one upstream chat completion, as bytes on the wire. A provider
// that signs completions (NEAR AI) signs the SHA-256 of exactly these bytes,
// which Eino never exposes once it has parsed them, so they're hashed here.
type Call struct {
	// Model is the upstream model name from the request body.
	Model          string
	CompletionID   string
	RequestSHA256  string
	ResponseSHA256 string
	// Complete is false when the body wasn't read to the end (a dropped
	// stream, or a client that went away); its hash then means nothing.
	Complete bool
}

// CallSink collects the calls made with a context from WithCalls, in order.
type CallSink struct {
	mu    sync.Mutex
	calls []Call
}

func (s *CallSink) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

type callKey struct{}

// WithCalls attaches a sink that records every chat completion made with the
// returned context.
func WithCalls(ctx context.Context) (context.Context, *CallSink) {
	s := &CallSink{}
	return context.WithValue(ctx, callKey{}, s), s
}

// capture hashes the request body and wraps the response so it is hashed as
// it is read. It only touches chat completions on a context with a sink.
func capture(r *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	sink, ok := r.Context().Value(callKey{}).(*CallSink)
	if !ok || r.Body == nil || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		return next(r)
	}
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	r.ContentLength = int64(len(body))

	sum := sha256.Sum256(body)
	var head struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &head)

	resp, err := next(r)
	if err != nil || resp.StatusCode != http.StatusOK {
		return resp, err
	}
	sink.mu.Lock()
	idx := len(sink.calls)
	sink.calls = append(sink.calls, Call{Model: head.Model, RequestSHA256: hex.EncodeToString(sum[:])})
	sink.mu.Unlock()
	resp.Body = &hashReader{ReadCloser: resp.Body, h: sha256.New(), sink: sink, idx: idx}
	return resp, nil
}

// hashReader hashes a response body as it passes through and picks the
// completion ID off its first JSON object.
type hashReader struct {
	io.ReadCloser
	h    hash.Hash
	sink *CallSink
	idx  int
	head []byte // bytes until the ID is found
	id   string
	done bool
}

const maxIDScan = 64 << 10

func (h *hashReader) Read(p []byte) (int, error) {
	n, err := h.ReadCloser.Read(p)
	h.h.Write(p[:n])
	if h.id == "" && len(h.head) < maxIDScan {
		h.head = append(h.head, p[:n]...)
		h.id = completionID(h.head)
	}
	if errors.Is(err, io.EOF) && !h.done {
		h.done = true
		h.finish(true)
	}
	return n, err
}

func (h *hashReader) Close() error {
	if !h.done {
		h.done = true
		h.finish(false)
	}
	return h.ReadCloser.Close()
}

func (h *hashReader) finish(complete bool) {
	if h.id == "" {
		h.id = completionID(h.head)
	}
	h.sink.mu.Lock()
	c := &h.sink.calls[h.idx]
	c.CompletionID = h.id
	c.ResponseSHA256 = hex.EncodeToString(h.h.Sum(nil))
	c.Complete = complete && h.id != ""
	h.sink.mu.Unlock()
	h.head = nil
}

// completionID finds "id" in the first complete JSON object of a body: the
// first SSE data line of a stream, or the whole body otherwise.
func completionID(b []byte) string {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		var line []byte
		if i < 0 {
			line = b
		} else {
			line = b[:i]
		}
		line = bytes.TrimSpace(line)
		line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(line) > 0 && line[0] == '{' {
			var v struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(line, &v) == nil && v.ID != "" {
				return v.ID
			}
		}
		if i < 0 {
			break
		}
		b = b[i+1:]
	}
	return ""
}

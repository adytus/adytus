package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing/iotest"
	"time"
	"testing"

	"github.com/cloudwego/eino/schema"
)

const testStream = "data: {\"id\":\"chatcmpl-42\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hel\"}}]}\n\n" +
	"data: {\"id\":\"chatcmpl-42\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: [DONE]\n\n"

func hexSHA(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// The hashes must be of the exact bytes on the wire: those are what a
// provider signs.
func TestCaptureHashesWireBytes(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, testStream)
	}))
	defer srv.Close()

	cm, err := NewChatModel(context.Background(), NearAI, srv.URL+"/v1", "k")
	if err != nil {
		t.Fatal(err)
	}
	ctx, sink := WithCalls(context.Background())
	sr, err := cm.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for {
		m, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		text += m.Content
	}
	sr.Close()
	if text != "Hello" {
		t.Fatalf("text %q", text)
	}

	calls := sink.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls %+v", calls)
	}
	c := calls[0]
	if c.CompletionID != "chatcmpl-42" || !c.Complete {
		t.Fatalf("call %+v", c)
	}
	if c.RequestSHA256 != hexSHA(gotBody) {
		t.Fatal("request hash isn't the hash of the bytes the server received")
	}
	if c.ResponseSHA256 != hexSHA([]byte(testStream)) {
		t.Fatal("response hash isn't the hash of the bytes the server sent")
	}

	// Without a sink nothing is recorded, and nothing breaks.
	sr, err = cm.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatal(err)
	}
	sr.Close()
	if len(sink.Calls()) != 1 {
		t.Fatal("recorded a call made without the sink")
	}
}

func TestCaptureMarksCutStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, testStream[:120])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // drop the connection mid-stream
	}))
	defer srv.Close()
	cm, err := NewChatModel(context.Background(), NearAI, srv.URL+"/v1", "k")
	if err != nil {
		t.Fatal(err)
	}
	ctx, sink := WithCalls(context.Background())
	sr, err := cm.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
	if err == nil {
		for {
			if _, err := sr.Recv(); err != nil {
				break
			}
		}
		sr.Close()
	}
	calls := sink.Calls()
	if len(calls) != 1 || calls[0].Complete {
		t.Fatalf("a cut stream counted as complete: %+v", calls)
	}
}

func TestCompletionID(t *testing.T) {
	cases := map[string]string{
		testStream:                               "chatcmpl-42",
		`{"id":"cmpl-9","choices":[]}`:           "cmpl-9",
		": keep-alive\n\ndata: {\"id\":\"x\"}\n": "x",
		"data: [DONE]\n":                         "",
	}
	for in, want := range cases {
		if got := completionID([]byte(in)); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// wrapped runs capture against a fake upstream that answers with body.
func wrapped(t *testing.T, body io.ReadCloser) (io.ReadCloser, *CallSink) {
	t.Helper()
	ctx, sink := WithCalls(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://x/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	resp, err := capture(req, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Body, sink
}

// readToDone reads like an SSE client: until it has seen "data: [DONE]".
func readToDone(t *testing.T, r io.Reader) {
	t.Helper()
	var got []byte
	buf := make([]byte, 7)
	for !strings.Contains(string(got), "data: [DONE]") {
		n, err := r.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, got)
		}
	}
}

// The "Not signed" bug: a client that stops at [DONE] and closes without
// reading to EOF must still get a complete call hashed over the whole body.
func TestCaptureCloseAfterDoneDrains(t *testing.T) {
	body, sink := wrapped(t, io.NopCloser(iotest.OneByteReader(strings.NewReader(testStream))))
	readToDone(t, body)
	body.Close()
	c := sink.Calls()[0]
	if !c.Complete || c.CompletionID != "chatcmpl-42" {
		t.Fatalf("call %+v", c)
	}
	if c.ResponseSHA256 != hexSHA([]byte(testStream)) {
		t.Fatal("response hash isn't over the full body")
	}
}

// A body that stalls after [DONE] isn't complete, and Close doesn't hang.
func TestCaptureCloseStalledStream(t *testing.T) {
	defer func(d time.Duration) { drainTimeout = d }(drainTimeout)
	drainTimeout = 50 * time.Millisecond
	pr, pw := io.Pipe()
	go io.WriteString(pw, testStream) // never closed
	body, sink := wrapped(t, pr)
	readToDone(t, body)
	start := time.Now()
	body.Close()
	if time.Since(start) > time.Second {
		t.Fatal("Close hung on a stalled body")
	}
	if sink.Calls()[0].Complete {
		t.Fatal("a stalled stream counted as complete")
	}
}

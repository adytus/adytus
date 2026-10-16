package server

// Integration tests: real Postgres, fake LLM, in-memory blob store.
// Run with TEST_DATABASE_URL pointing at a disposable database, e.g.
//
//	TEST_DATABASE_URL=postgres://adytus:adytus@localhost:5432/adytus_test?sslmode=disable go test ./...

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/reaganiwadha/adytus/apps/api/internal/billing"
	"github.com/reaganiwadha/adytus/apps/api/internal/blob"
	"github.com/reaganiwadha/adytus/apps/api/internal/config"
	"github.com/reaganiwadha/adytus/apps/api/internal/crypto"
	"github.com/reaganiwadha/adytus/apps/api/internal/db"
	"github.com/reaganiwadha/adytus/apps/api/internal/llm"
	"github.com/reaganiwadha/adytus/apps/api/internal/session"
	"github.com/reaganiwadha/adytus/apps/api/internal/store"
	"github.com/reaganiwadha/adytus/apps/api/internal/vault"
)

// fakeLLM echoes the last user message and reports fixed usage.
type fakeLLM struct {
	mu      sync.Mutex
	lastReq llm.ChatRequest
	fail    bool
	noUsage bool
}

func (f *fakeLLM) Stream(_ context.Context, req llm.ChatRequest, onDelta func(string) error) (*llm.ChatResponse, error) {
	f.mu.Lock()
	f.lastReq = req
	fail, noUsage := f.fail, f.noUsage
	f.mu.Unlock()
	if fail {
		return nil, io.ErrUnexpectedEOF
	}
	last := req.Messages[len(req.Messages)-1].Content
	reply := "echo: " + last
	for _, part := range []string{reply[:len(reply)/2], reply[len(reply)/2:]} {
		if err := onDelta(part); err != nil {
			return &llm.ChatResponse{Content: part}, err
		}
	}
	resp := &llm.ChatResponse{Content: reply}
	if !noUsage {
		resp.Usage = llm.Usage{PromptTokens: 1000, CompletionTokens: 100, Reported: true}
	}
	return resp, nil
}

var testModels = []llm.Model{
	{ID: "cheap", Upstream: "up-cheap", PromptUSDPerMTok: 1, CompletionUSDPerMTok: 2, ContextTokens: 8000, MaxOutputTokens: 500},
	// $100 per million output tokens: a 500-token hold costs $0.05.
	{ID: "pricey", Upstream: "up-pricey", PromptUSDPerMTok: 50, CompletionUSDPerMTok: 100, ContextTokens: 8000, MaxOutputTokens: 500},
}

type env struct {
	t      *testing.T
	srv    *Server
	llm    *fakeLLM
	blobs  *blob.Memory
	pool   *pgxpool.Pool
	cookie string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	reset, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reset.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatal(err)
	}
	reset.Close()

	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	serverKey, _ := crypto.NewKey()
	cfg := &config.Config{
		ServerKey:      serverKey,
		SystemPrompt:   "sys",
		ChatPerMinute:  1000,
		AuthPerMinute:  1000,
		SessionTTL:     time.Hour,
		MaxUploadBytes: 1 << 20,
		KDF:            crypto.KDFParams{Time: 1, MemoryKiB: 1024, Threads: 1},
	}
	catalog, err := llm.NewCatalog(testModels)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeLLM{}
	blobs := blob.NewMemory()
	srv := New(Deps{
		Config:   cfg,
		Store:    &store.Store{DB: pool},
		Ledger:   &billing.Ledger{DB: pool, HoldTTL: time.Minute},
		Catalog:  catalog,
		LLM:      fake,
		Payments: billing.MockGateway{},
		Sessions: session.NewStore(time.Hour, time.Hour),
		Blobs:    blobs,
	})
	return &env{t: t, srv: srv, llm: fake, blobs: blobs, pool: pool}
}

func (e *env) do(method, path string, body any) (*http.Response, map[string]any) {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return e.send(req)
}

func (e *env) send(req *http.Request) (*http.Response, map[string]any) {
	e.t.Helper()
	if e.cookie != "" {
		req.Header.Set("Cookie", sessionCookie+"="+e.cookie)
	}
	resp, err := e.srv.App.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		e.t.Fatal(err)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == sessionCookie {
			e.cookie = ck.Value
		}
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp, out
}

func (e *env) must(status int, method, path string, body any) map[string]any {
	e.t.Helper()
	resp, out := e.do(method, path, body)
	if resp.StatusCode != status {
		raw, _ := io.ReadAll(resp.Body)
		e.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, status, raw)
	}
	return out
}

func (e *env) signup(email string) map[string]any {
	return e.must(201, "POST", "/api/auth/signup", map[string]string{"email": email, "password": "correct horse battery"})
}

type sseEvent struct {
	Name string
	Data map[string]any
}

func (e *env) sendMessage(chatID, content string, fileIDs ...string) (int, []sseEvent) {
	e.t.Helper()
	body := map[string]any{"content": content}
	if len(fileIDs) > 0 {
		body["file_ids"] = fileIDs
	}
	resp, _ := e.do("POST", "/api/chats/"+chatID+"/messages", body)
	if resp.StatusCode != 200 {
		return resp.StatusCode, nil
	}
	var events []sseEvent
	sc := bufio.NewScanner(resp.Body)
	var cur sseEvent
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			cur.Name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &cur.Data)
		case line == "":
			if cur.Name != "" {
				events = append(events, cur)
			}
			cur = sseEvent{}
		}
	}
	return 200, events
}

func last(events []sseEvent) sseEvent {
	if len(events) == 0 {
		return sseEvent{}
	}
	return events[len(events)-1]
}

func num(v any) int64 { f, _ := v.(float64); return int64(f) }

func (e *env) available() int64 {
	out := e.must(200, "GET", "/api/billing/balance", nil)
	return num(out["balance"].(map[string]any)["available_micros"])
}

func TestChatFlowChargesAndEncrypts(t *testing.T) {
	e := newEnv(t)
	out := e.signup("a@example.com")
	if code, _ := out["recovery_code"].(string); len(code) != 39 {
		t.Fatalf("missing recovery code: %v", out)
	}

	chat := e.must(201, "POST", "/api/chats", map[string]string{"model": "cheap"})["chat"].(map[string]any)
	chatID := chat["id"].(string)

	// No credit yet.
	if status, _ := e.sendMessage(chatID, "hi"); status != http.StatusPaymentRequired {
		t.Fatalf("expected 402 without credit, got %d", status)
	}

	e.must(200, "POST", "/api/billing/subscribe", map[string]string{"plan_id": "standard"})
	if got := e.available(); got != 5_000_000 {
		t.Fatalf("available after subscribe = %d", got)
	}

	status, events := e.sendMessage(chatID, "the secret launch code is 0000")
	if status != 200 || last(events).Name != "done" {
		t.Fatalf("send failed: %d %+v", status, events)
	}
	if events[0].Name != "start" || events[1].Name != "delta" {
		t.Fatalf("unexpected event order: %+v", events)
	}
	// 1000 prompt * $1/M + 100 completion * $2/M = 1000 + 200 micro-USD.
	if got := e.available(); got != 5_000_000-1200 {
		t.Fatalf("available after chat = %d", got)
	}

	// System prompt is sent with the configured upstream model and cap.
	if e.llm.lastReq.Model != "up-cheap" || e.llm.lastReq.MaxTokens != 500 || e.llm.lastReq.Messages[0].Role != "system" {
		t.Fatalf("unexpected upstream request: %+v", e.llm.lastReq)
	}

	got := e.must(200, "GET", "/api/chats/"+chatID, nil)
	msgs := got["messages"].([]any)
	if len(msgs) != 2 || msgs[1].(map[string]any)["content"] != "echo: the secret launch code is 0000" {
		t.Fatalf("messages: %+v", msgs)
	}
	if got["chat"].(map[string]any)["title"] != "the secret launch code is 0000" {
		t.Fatalf("title not set: %v", got["chat"])
	}

	// Nothing readable is stored.
	rows, _ := e.pool.Query(context.Background(), `SELECT ciphertext FROM objects`)
	for rows.Next() {
		var ct []byte
		_ = rows.Scan(&ct)
		if bytes.Contains(ct, []byte("secret")) || bytes.Contains(ct, []byte("launch")) {
			t.Fatal("plaintext found in objects table")
		}
	}
	rows.Close()

	ledger := e.must(200, "GET", "/api/billing/ledger", nil)["entries"].([]any)
	usage := ledger[0].(map[string]any)
	if usage["reason"] != "usage" || num(usage["delta_micros"]) != -1200 || num(usage["prompt_tokens"]) != 1000 {
		t.Fatalf("ledger: %+v", usage)
	}
}

func TestBudgetHoldsAndCaps(t *testing.T) {
	e := newEnv(t)
	e.signup("b@example.com")
	e.must(200, "POST", "/api/billing/subscribe", map[string]string{"plan_id": "standard"})
	chatID := e.must(201, "POST", "/api/chats", map[string]string{"model": "pricey"})["chat"].(map[string]any)["id"].(string)

	// Daily cap below one request's hold ($0.05 + prompt).
	e.srv.Ledger.DailyCapMicros = 40_000
	status, _ := e.sendMessage(chatID, "hi")
	if status != http.StatusPaymentRequired {
		t.Fatalf("expected daily cap to refuse, got %d", status)
	}
	_, body := e.do("POST", "/api/chats/"+chatID+"/messages", map[string]string{"content": "hi"})
	if !strings.Contains(body["message"].(string), "daily") {
		t.Fatalf("expected daily cap message, got %v", body)
	}

	// Without the cap the request goes through and is billed at actual usage.
	e.srv.Ledger.DailyCapMicros = 0
	before := e.available()
	if status, ev := e.sendMessage(chatID, "hi"); status != 200 || last(ev).Name != "done" {
		t.Fatalf("send: %d %+v", status, ev)
	}
	// 1000*50 + 100*100 = 60000
	if spent := before - e.available(); spent != 60_000 {
		t.Fatalf("spent %d", spent)
	}

	// Holds are gone after settling.
	var holds int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM credit_holds`).Scan(&holds)
	if holds != 0 {
		t.Fatalf("%d holds left", holds)
	}

	// An upstream failure releases the hold and charges nothing.
	e.llm.fail = true
	before = e.available()
	_, ev := e.sendMessage(chatID, "hi")
	if last(ev).Name != "error" || e.available() != before {
		t.Fatalf("failure should not charge: %+v", ev)
	}
	e.llm.fail = false

	// No usage reported (e.g. cancelled): billed by estimate, not free.
	e.llm.noUsage = true
	before = e.available()
	e.sendMessage(chatID, "hi")
	if e.available() >= before {
		t.Fatal("unreported usage must still be charged")
	}
}

func TestReserveIsConcurrencySafe(t *testing.T) {
	e := newEnv(t)
	out := e.signup("c@example.com")
	e.must(200, "POST", "/api/billing/subscribe", map[string]string{"plan_id": "standard"})
	userID := mustUUID(t, out["user"].(map[string]any)["id"].(string))

	// $5 of credit, 20 concurrent $1 holds: exactly 5 may succeed.
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.srv.Ledger.Reserve(context.Background(), userID, 1_000_000); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 5 {
		t.Fatalf("%d holds succeeded, want 5", ok)
	}
}

func TestAuthAndRecovery(t *testing.T) {
	e := newEnv(t)
	code := e.signup("d@example.com")["recovery_code"].(string)
	e.must(200, "POST", "/api/billing/subscribe", map[string]string{"plan_id": "standard"})
	chatID := e.must(201, "POST", "/api/chats", map[string]string{"title": "keep me"})["chat"].(map[string]any)["id"].(string)

	e.must(409, "POST", "/api/auth/signup", map[string]string{"email": "D@example.com", "password": "another long password"})

	e.must(204, "POST", "/api/auth/logout", nil)
	e.must(401, "GET", "/api/me", nil)

	e.must(401, "POST", "/api/auth/login", map[string]string{"email": "d@example.com", "password": "wrong password here"})
	e.must(401, "POST", "/api/auth/login", map[string]string{"email": "nobody@example.com", "password": "correct horse battery"})
	e.must(200, "POST", "/api/auth/login", map[string]string{"email": "d@example.com", "password": "correct horse battery"})

	// Change password; data stays readable.
	out := e.must(200, "POST", "/api/auth/password", map[string]string{"current_password": "correct horse battery", "new_password": "a brand new passphrase"})
	newCode := out["recovery_code"].(string)
	if newCode == code {
		t.Fatal("recovery code should rotate")
	}
	e.must(204, "POST", "/api/auth/logout", nil)
	e.must(401, "POST", "/api/auth/login", map[string]string{"email": "d@example.com", "password": "correct horse battery"})

	// Old recovery code no longer works; the new one does.
	e.must(401, "POST", "/api/auth/recover", map[string]string{"email": "d@example.com", "recovery_code": code, "new_password": "recovered passphrase"})
	e.must(200, "POST", "/api/auth/recover", map[string]string{"email": "d@example.com", "recovery_code": strings.ToLower(newCode), "new_password": "recovered passphrase"})

	chat := e.must(200, "GET", "/api/chats/"+chatID, nil)["chat"].(map[string]any)
	if chat["title"] != "keep me" {
		t.Fatalf("data lost after recovery: %v", chat)
	}
}

func TestIsolationBetweenUsers(t *testing.T) {
	e := newEnv(t)
	e.signup("e@example.com")
	chatID := e.must(201, "POST", "/api/chats", nil)["chat"].(map[string]any)["id"].(string)

	e.cookie = ""
	e.signup("f@example.com")
	e.must(404, "GET", "/api/chats/"+chatID, nil)
	e.must(404, "DELETE", "/api/chats/"+chatID, nil)
	if chats := e.must(200, "GET", "/api/chats", nil)["chats"].([]any); len(chats) != 0 {
		t.Fatalf("saw another user's chats: %v", chats)
	}
}

func (e *env) upload(name string, data []byte) map[string]any {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(data)
	mw.Close()
	req := httptest.NewRequest("POST", "/api/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, out := e.send(req)
	if resp.StatusCode != 201 {
		e.t.Fatalf("upload: %d %v", resp.StatusCode, out)
	}
	return out["file"].(map[string]any)
}

func TestFilesEncryptedAttachedAndOfflineOptIn(t *testing.T) {
	e := newEnv(t)
	e.signup("g@example.com")
	e.must(200, "POST", "/api/billing/subscribe", map[string]string{"plan_id": "standard"})

	file := e.upload("notes.md", []byte("quarterly numbers: 42"))
	fileID := file["id"].(string)
	if file["text"] != true || file["name"] != "notes.md" {
		t.Fatalf("file meta: %v", file)
	}
	bin := e.upload("img.bin", []byte{0, 1, 2, 3})

	// Blob store holds only ciphertext.
	for _, v := range e.blobs.Keys() {
		if bytes.Contains(v, []byte("quarterly")) {
			t.Fatal("plaintext in blob store")
		}
	}

	resp, _ := e.do("GET", "/api/files/"+fileID+"/content", nil)
	raw, _ := io.ReadAll(resp.Body)
	if string(raw) != "quarterly numbers: 42" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("download: %q %v", raw, resp.Header)
	}

	// Text files are inlined into the prompt; binary files are refused.
	chatID := e.must(201, "POST", "/api/chats", nil)["chat"].(map[string]any)["id"].(string)
	if status, ev := e.sendMessage(chatID, "summarize", fileID); status != 200 || last(ev).Name != "done" {
		t.Fatalf("send with attachment: %d", status)
	}
	sent := e.llm.lastReq.Messages[len(e.llm.lastReq.Messages)-1].Content
	if !strings.Contains(sent, "quarterly numbers: 42") || !strings.Contains(sent, `name="notes.md"`) {
		t.Fatalf("attachment not inlined: %q", sent)
	}
	if status, _ := e.sendMessage(chatID, "x", bin["id"].(string)); status != 400 {
		t.Fatalf("binary attachment should be refused, got %d", status)
	}

	// Offline opt-in: the server key can read the file only after opt-in.
	ctx := context.Background()
	s := e.srv.Store
	userID := mustUUID(t, e.must(200, "GET", "/api/me", nil)["user"].(map[string]any)["id"].(string))
	obj, _ := s.GetObject(ctx, userID, mustUUID(t, fileID), store.KindFile)
	if _, err := vault.OfflineDEK(e.srv.Config.ServerKey, obj); err == nil {
		t.Fatal("server read a file that is not opted in")
	}
	e.must(200, "PUT", "/api/files/"+fileID+"/offline", map[string]bool{"enabled": true})
	obj, _ = s.GetObject(ctx, userID, mustUUID(t, fileID), store.KindFile)
	if _, err := vault.OfflineDEK(e.srv.Config.ServerKey, obj); err != nil {
		t.Fatalf("server should read opted-in file: %v", err)
	}
	e.must(200, "PUT", "/api/files/"+fileID+"/offline", map[string]bool{"enabled": false})
	obj, _ = s.GetObject(ctx, userID, mustUUID(t, fileID), store.KindFile)
	if obj.ServerWrappedDEK != nil {
		t.Fatal("opt-out should remove the server-wrapped key")
	}

	// Chat opt-in covers its messages, including ones sent later.
	e.must(200, "PUT", "/api/chats/"+chatID+"/offline", map[string]bool{"enabled": true})
	e.sendMessage(chatID, "after opt-in")
	var missing int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM objects WHERE (id = $1 OR parent_id = $1) AND server_wrapped_dek IS NULL`, chatID).Scan(&missing)
	if missing != 0 {
		t.Fatalf("%d chat objects not opted in", missing)
	}

	e.must(204, "DELETE", "/api/files/"+fileID, nil)
	if len(e.blobs.Keys()) != 1 {
		t.Fatalf("blob not deleted: %d left", len(e.blobs.Keys()))
	}
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

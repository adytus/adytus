package server

// Integration tests: real Postgres, fake LLM, in-memory blob store.
// Run with TEST_DATABASE_URL pointing at a disposable database, e.g.
//
//	TEST_DATABASE_URL=postgres://adytus:adytus@localhost:5432/adytus_test?sslmode=disable go test ./...

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	pngenc "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/reaganiwadha/adytus/apps/api/internal/billing"
	"github.com/reaganiwadha/adytus/apps/api/internal/blob"
	"github.com/reaganiwadha/adytus/apps/api/internal/config"
	"github.com/reaganiwadha/adytus/apps/api/internal/crypto"
	"github.com/reaganiwadha/adytus/apps/api/internal/db"
	"github.com/reaganiwadha/adytus/apps/api/internal/imageprep"
	"github.com/reaganiwadha/adytus/apps/api/internal/llm"
	"github.com/reaganiwadha/adytus/apps/api/internal/meter"
	"github.com/reaganiwadha/adytus/apps/api/internal/session"
	"github.com/reaganiwadha/adytus/apps/api/internal/store"
	"github.com/reaganiwadha/adytus/apps/api/internal/tools"
	"github.com/reaganiwadha/adytus/apps/api/internal/vault"
)

// fakeLLM is an Eino chat model that echoes the last user message and
// reports fixed usage.
type fakeLLM struct {
	mu       sync.Mutex
	lastMsgs []*schema.Message
	lastOpts *model.Options
	// lastTools are the tool names offered on the last call.
	lastTools []string
	fail      bool
	noUsage   bool
	// preamble makes tool-calling replies write a sentence before the call,
	// as DeepSeek and Claude do.
	preamble bool
	// title is what Generate (only used to name chats) answers; empty fails.
	title string
}

func (f *fakeLLM) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	f.mu.Lock()
	title := f.title
	f.mu.Unlock()
	if title == "" {
		return nil, errors.New("not used")
	}
	out := schema.AssistantMessage(title, nil)
	out.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 100, CompletionTokens: 10}}
	return out, nil
}

func (f *fakeLLM) WithTools(infos []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	b := &boundLLM{fakeLLM: f}
	for _, i := range infos {
		b.tools = append(b.tools, i.Name)
	}
	return b, nil
}

// boundLLM is the fake with tools offered. "calc <expr>" makes it call the
// calculator; after a tool result it answers with that result.
type boundLLM struct {
	*fakeLLM
	tools []string
}

func (b *boundLLM) Stream(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return b.stream(msgs, opts, b.tools)
}

func (f *fakeLLM) Stream(_ context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return f.stream(msgs, opts, nil)
}

func (f *fakeLLM) stream(msgs []*schema.Message, opts []model.Option, offered []string) (*schema.StreamReader[*schema.Message], error) {
	f.mu.Lock()
	f.lastMsgs, f.lastOpts, f.lastTools = msgs, model.GetCommonOptions(nil, opts...), offered
	fail, noUsage, preamble := f.fail, f.noUsage, f.preamble
	f.mu.Unlock()
	if fail {
		return nil, io.ErrUnexpectedEOF
	}
	usage := &schema.Message{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{
		Usage: &schema.TokenUsage{PromptTokens: 1000, CompletionTokens: 100},
	}}
	lastMsg := msgs[len(msgs)-1]
	if args, ok := strings.CutPrefix(lastLine(text(lastMsg)), "py "); ok && slices.Contains(offered, "python") {
		idx := 0
		call := schema.AssistantMessage("", []schema.ToolCall{{Index: &idx, ID: "call_py", Type: "function",
			Function: schema.FunctionCall{Name: "python", Arguments: args}}})
		return schema.StreamReaderFromArray([]*schema.Message{call, usage}), nil
	}
	if expr, ok := strings.CutPrefix(text(lastMsg), "calc "); ok && slices.Contains(offered, "calculator") {
		args, _ := json.Marshal(map[string]string{"expression": expr})
		idx := 0
		call := schema.AssistantMessage("", []schema.ToolCall{{Index: &idx, ID: "call_1", Type: "function",
			Function: schema.FunctionCall{Name: "calculator", Arguments: string(args)}}})
		call.ReasoningContent = "need the calculator"
		chunks := []*schema.Message{call, usage}
		if preamble {
			chunks = append([]*schema.Message{schema.AssistantMessage("I'll work that out. ", nil)}, chunks...)
		}
		return schema.StreamReaderFromArray(chunks), nil
	}
	reply := "echo: " + text(lastMsg)
	if lastMsg.Role == schema.Tool {
		reply = "answer: " + lastMsg.Content
	}
	chunks := []*schema.Message{
		schema.AssistantMessage(reply[:len(reply)/2], nil),
		schema.AssistantMessage(reply[len(reply)/2:], nil),
	}
	if !noUsage {
		chunks = append(chunks, usage)
	}
	return schema.StreamReaderFromArray(chunks), nil
}

// text is a message's text, whether plain or in content parts.
func text(m *schema.Message) string {
	if len(m.UserInputMultiContent) == 0 {
		return m.Content
	}
	var sb strings.Builder
	for _, p := range m.UserInputMultiContent {
		sb.WriteString(p.Text)
	}
	return sb.String()
}

var testModels = []llm.Model{
	{ID: "cheap", Upstream: "up-cheap", PromptUSDPerMTok: 1, CompletionUSDPerMTok: 2, ContextTokens: 8000, MaxOutputTokens: 500},
	// $100 per million output tokens: a 500-token hold costs $0.05.
	{ID: "pricey", Upstream: "up-pricey", PromptUSDPerMTok: 50, CompletionUSDPerMTok: 100, ContextTokens: 8000, MaxOutputTokens: 500},
	{ID: "hands", Upstream: "up-hands", PromptUSDPerMTok: 1, CompletionUSDPerMTok: 2, ContextTokens: 8000, MaxOutputTokens: 500, Tools: true},
	{ID: "eyes", Upstream: "up-eyes", PromptUSDPerMTok: 1, CompletionUSDPerMTok: 2, ContextTokens: 8000, MaxOutputTokens: 500, Vision: true, ImageTokens: 1500},
}

type env struct {
	t      *testing.T
	srv    *Server
	llm    *fakeLLM
	blobs  *blob.Memory
	pool   *pgxpool.Pool
	cookie string
}

func newEnv(t *testing.T) *env { return newEnvWith(t, nil) }

// newEnvWith sets up a server whose python tool runs on py; the document
// reader comes with it, as in production.
func newEnvWith(t *testing.T, py tools.PythonRunner) *env {
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
	uiDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(uiDir, "backoffice.js"), []byte("export function mount() {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		BackofficeDir:  uiDir,
		ServerKey:      serverKey,
		SystemPrompt:   "sys",
		ChatPerMinute:  1000,
		AuthPerMinute:  1000,
		SessionTTL:     time.Hour,
		MaxUploadBytes: 1 << 20,
		KDF:            crypto.KDFParams{Time: 1, MemoryKiB: 1024, Threads: 1},
		ToolMaxSteps:   4,
	}
	var docs *tools.Documents
	if py != nil {
		docs = tools.NewDocuments()
	}
	registry, err := tools.NewRegistry(nil, py, docs)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeLLM{}
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	t.Cleanup(ollama.Close)
	blobs := blob.NewMemory()
	srv := New(Deps{
		Config: cfg,
		Store:  &store.Store{DB: pool},
		Ledger: &billing.Ledger{DB: pool, HoldTTL: time.Minute},
		LLM: llm.NewRegistry(func(context.Context, llm.Provider) (model.ToolCallingChatModel, error) {
			return fake, nil
		}),
		Sessions: session.NewStore(time.Hour, time.Hour),
		Blobs:    blobs,
		Tools:    registry,
	})
	models := append([]llm.Model(nil), testModels...)
	for i := range models {
		models[i].Provider = llm.Ollama
	}
	if err := srv.SeedLLM(ctx, llm.Provider{ID: llm.Ollama, BaseURL: ollama.URL}, func() (*llm.Catalog, error) {
		return llm.NewCatalog(models)
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.ReloadLLM(ctx); err != nil {
		t.Fatal(err)
	}
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

func TestVisibleModelsPreferDeepSeekAndHideOfflineOllama(t *testing.T) {
	models := []llm.Model{
		{ID: "qwen", Provider: llm.Ollama},
		{ID: preferredModelID, Provider: llm.OpenRouter},
		{ID: "glm", Provider: llm.OpenRouter},
	}
	got := visibleModels(models, false)
	if len(got) != 2 || got[0].ID != preferredModelID || got[1].ID != "glm" {
		t.Fatalf("without Ollama: %v", got)
	}
	got = visibleModels(models, true)
	if len(got) != 3 || got[0].ID != preferredModelID {
		t.Fatalf("with Ollama: %v", got)
	}
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

	e.fund()
	if got := e.available(); got != 5_000_000 {
		t.Fatalf("available after funding = %d", got)
	}

	status, events := e.sendMessage(chatID, "the secret launch code is 0000")
	if status != 200 || last(events).Name != "done" {
		t.Fatalf("send failed: %d %+v", status, events)
	}
	if events[0].Name != "start" || events[1].Name != "delta" {
		t.Fatalf("unexpected event order: %+v", events)
	}
	// 1000 prompt * $1/M + 100 completion * $2/M = 1200 micro-USD, which
	// rounds up to 2 tokens ($0.002).
	if got := e.available(); got != 5_000_000-2_000 {
		t.Fatalf("available after chat = %d", got)
	}

	// System prompt is sent with the configured upstream model and cap.
	if o := e.llm.lastOpts; *o.Model != "up-cheap" || *o.MaxTokens != 500 || e.llm.lastMsgs[0].Role != schema.System {
		t.Fatalf("unexpected upstream request: model %s, max %d, first %+v", *o.Model, *o.MaxTokens, e.llm.lastMsgs[0])
	}

	got := e.must(200, "GET", "/api/chats/"+chatID, nil)
	msgs := got["messages"].([]any)
	if len(msgs) != 2 || msgs[1].(map[string]any)["content"] != "echo: the secret launch code is 0000" {
		t.Fatalf("messages: %+v", msgs)
	}
	if usage := msgs[1].(map[string]any)["usage"].(map[string]any); usage["cost_micros"] != nil {
		t.Fatalf("non-admin saw the message cost: %v", usage)
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
	if usage["reason"] != "usage" || num(usage["delta_micros"]) != -2000 || num(usage["delta_tokens"]) != -2 || num(usage["prompt_tokens"]) != 1000 {
		t.Fatalf("ledger: %+v", usage)
	}
}

func TestBudgetHoldsAndCaps(t *testing.T) {
	e := newEnv(t)
	e.signup("b@example.com")
	e.fund()
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
	e.fund()
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
	e.fund()
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
	e.fund()

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
	sent := text(e.llm.lastMsgs[len(e.llm.lastMsgs)-1])
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

func TestImageAttachmentsNeedVision(t *testing.T) {
	e := newEnv(t)
	e.signup("h@example.com")
	e.fund()

	var png bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2000, 1000))
	if err := pngenc.Encode(&png, img); err != nil {
		t.Fatal(err)
	}
	file := e.upload("photo.png", png.Bytes())
	if file["image"] != true || file["text"] != false {
		t.Fatalf("file meta: %v", file)
	}
	fileID := file["id"].(string)

	chatID := e.must(201, "POST", "/api/chats", nil)["chat"].(map[string]any)["id"].(string)
	if status, _ := e.sendMessage(chatID, "what is this?", fileID); status != 400 {
		t.Fatalf("image on a text-only model should be refused, got %d", status)
	}

	e.must(200, "PATCH", "/api/chats/"+chatID, map[string]string{"model": "eyes"})
	if status, ev := e.sendMessage(chatID, "what is this?", fileID); status != 200 || last(ev).Name != "done" {
		t.Fatalf("send with image: %d", status)
	}
	parts := e.llm.lastMsgs[len(e.llm.lastMsgs)-1].UserInputMultiContent
	if len(parts) != 2 || parts[0].Text != "what is this?" || parts[1].Image == nil {
		t.Fatalf("unexpected parts: %+v", parts)
	}
	im := parts[1].Image
	raw, err := base64.StdEncoding.DecodeString(*im.Base64Data)
	if err != nil || im.MIMEType != "image/jpeg" {
		t.Fatalf("image part: %s %v", im.MIMEType, err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width != imageprep.MaxSide || cfg.Height != imageprep.MaxSide/2 {
		t.Fatalf("image not downscaled: %+v %v", cfg, err)
	}

	// The image stays in the history for the next turn.
	e.sendMessage(chatID, "and now?")
	var images int
	for _, m := range e.llm.lastMsgs {
		for _, p := range m.UserInputMultiContent {
			if p.Image != nil {
				images++
			}
		}
	}
	if images != 1 {
		t.Fatalf("history images = %d", images)
	}
}

func TestToolSettingsAndToolLoop(t *testing.T) {
	e := newEnv(t)
	e.signup("t@example.com")
	e.fund()

	st := e.must(200, "GET", "/api/settings", nil)["tools"].([]any)
	calc := st[0].(map[string]any)
	if len(st) != 1 || calc["id"] != "calculator" || calc["enabled"] != true {
		t.Fatalf("settings: %v", st)
	}
	if r, _ := e.do("PUT", "/api/settings", map[string]any{"tools": map[string]bool{"rm_rf": true}}); r.StatusCode != 400 {
		t.Fatalf("unknown tool accepted: %d", r.StatusCode)
	}

	chatID := e.must(201, "POST", "/api/chats", map[string]string{"model": "hands"})["chat"].(map[string]any)["id"].(string)
	before := e.available()
	status, ev := e.sendMessage(chatID, "calc 6*7")
	if status != 200 || last(ev).Name != "done" {
		t.Fatalf("send: %d %v", status, ev)
	}
	var toolEvents, reasoning int
	for _, x := range ev {
		switch x.Name {
		case "tool":
			toolEvents++
		case "reasoning":
			reasoning++
		}
	}
	if toolEvents != 2 {
		t.Fatalf("want a started and a finished tool event, got %d", toolEvents)
	}
	msg := last(ev).Data["message"].(map[string]any)
	if msg["content"] != "answer: 42" {
		t.Fatalf("reply: %v", msg["content"])
	}
	steps := msg["steps"].([]any)
	step := steps[0].(map[string]any)
	if len(steps) != 1 || step["tool"] != "calculator" || step["output"] != "42" || step["done"] != true {
		t.Fatalf("steps: %v", steps)
	}
	// Both model calls are billed: 2 x (1000 prompt + 100 completion), but
	// the per-message cost is backoffice-only.
	usage := msg["usage"].(map[string]any)
	if num(usage["prompt_tokens"]) != 2000 || before-e.available() <= 0 {
		t.Fatalf("usage %v, charged %d", usage, before-e.available())
	}
	if _, ok := usage["cost_micros"]; ok {
		t.Fatalf("non-admin saw the message cost: %v", usage)
	}

	// Steps come back with the chat.
	msgs := e.must(200, "GET", "/api/chats/"+chatID, nil)["messages"].([]any)
	if _, ok := msgs[1].(map[string]any)["steps"]; !ok {
		t.Fatalf("steps not stored: %v", msgs[1])
	}

	// Switched off, the tool is no longer offered.
	e.must(200, "PUT", "/api/settings", map[string]any{"tools": map[string]bool{"calculator": false}})
	e.sendMessage(chatID, "calc 1+1")
	if len(e.llm.lastTools) != 0 {
		t.Fatalf("tools offered after opting out: %v", e.llm.lastTools)
	}
}

func TestToolCallAfterPreambleRuns(t *testing.T) {
	e := newEnv(t)
	e.signup("p@example.com")
	e.fund()
	// DeepSeek and Claude write a sentence before the call; that must not be
	// mistaken for the final answer.
	e.llm.preamble = true

	chatID := e.must(201, "POST", "/api/chats", map[string]string{"model": "hands"})["chat"].(map[string]any)["id"].(string)
	status, ev := e.sendMessage(chatID, "calc 6*7")
	if status != 200 || last(ev).Name != "done" {
		t.Fatalf("send: %d %v", status, ev)
	}
	toolEvents := 0
	for _, x := range ev {
		if x.Name == "tool" {
			toolEvents++
		}
	}
	if toolEvents != 2 {
		t.Fatalf("tool call after a preamble was dropped: %v", ev)
	}
	msg := last(ev).Data["message"].(map[string]any)
	content, _ := msg["content"].(string)
	if !strings.Contains(content, "I'll work that out.") || !strings.Contains(content, "answer: 42") {
		t.Fatalf("reply: %q", content)
	}
	if steps := msg["steps"].([]any); len(steps) != 1 || steps[0].(map[string]any)["output"] != "42" {
		t.Fatalf("steps: %v", steps)
	}
}

// noopLedger lets meter.Model run without a database.
type noopLedger struct{}

func (noopLedger) Reserve(context.Context, uuid.UUID, int64) (uuid.UUID, error) {
	return uuid.New(), nil
}
func (noopLedger) Settle(context.Context, uuid.UUID, uuid.UUID, billing.Usage) error { return nil }
func (noopLedger) Release(context.Context, uuid.UUID) error                          { return nil }

// TestReplyRunsToolCallAfterPreamble covers the streaming loop directly, so it
// runs without a database too.
func TestReplyRunsToolCallAfterPreamble(t *testing.T) {
	registry, err := tools.NewRegistry(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Deps: Deps{Config: &config.Config{ToolMaxSteps: 4}, Tools: registry}}
	spec := llm.Model{
		ID: "hands", Upstream: "up", PromptUSDPerMTok: 1, CompletionUSDPerMTok: 2,
		ContextTokens: 8000, MaxOutputTokens: 500, Tools: true,
	}
	mm := meter.New(&fakeLLM{preamble: true}, noopLedger{}, uuid.Nil, spec)
	ctx := context.Background()
	infos, err := registry.ToolInfos(ctx, []string{tools.Calculator})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := mm.WithTools(infos)
	if err != nil {
		t.Fatal(err)
	}

	var steps []tools.Step
	sr, err := srv.reply(ctx, bound, []*schema.Message{
		schema.SystemMessage("sys"),
		schema.UserMessage("calc 6*7"),
	}, []string{tools.Calculator}, func(st tools.Step) { steps = append(steps, st) })
	if err != nil {
		t.Fatal(err)
	}
	var content strings.Builder
	for {
		msg, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content.WriteString(msg.Content)
	}
	sr.Close()

	if len(steps) != 2 || steps[1].Output != "42" || !steps[1].Done {
		t.Fatalf("steps: %+v", steps)
	}
	if !strings.Contains(content.String(), "answer: 42") {
		t.Fatalf("reply: %q", content.String())
	}
}

func TestImageThumbnails(t *testing.T) {
	e := newEnv(t)
	e.signup("p@example.com")

	var png bytes.Buffer
	if err := pngenc.Encode(&png, image.NewRGBA(image.Rect(0, 0, 1600, 800))); err != nil {
		t.Fatal(err)
	}
	file := e.upload("wide.png", png.Bytes())
	if file["thumb"] != true {
		t.Fatalf("no thumbnail: %v", file)
	}
	id := file["id"].(string)
	o, err := e.srv.Store.GetObject(context.Background(), e.userID(t), mustUUID(t, id), store.KindFile)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := e.blobs.Get(context.Background(), o.S3Key.ValueOrZero()+".thumb")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jpeg.DecodeConfig(bytes.NewReader(sealed)); err == nil {
		t.Fatal("thumbnail stored in the clear")
	}

	res, _ := e.do("GET", "/api/files/"+id+"/thumbnail", nil)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumbnail: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	cfg, err := jpeg.DecodeConfig(res.Body)
	if err != nil || cfg.Width != imageprep.ThumbSide || cfg.Height != imageprep.ThumbSide/2 {
		t.Fatalf("thumbnail size: %+v %v", cfg, err)
	}

	res, _ = e.do("GET", "/api/files/"+id+"/view", nil)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("view: %d", res.StatusCode)
	}
	if cfg, err := jpeg.DecodeConfig(res.Body); err != nil || cfg.Width != 1600 {
		t.Fatalf("view size: %+v %v", cfg, err)
	}

	text := e.upload("notes.txt", []byte("hello"))
	for _, path := range []string{"/thumbnail", "/view"} {
		if r, _ := e.do("GET", "/api/files/"+text["id"].(string)+path, nil); r.StatusCode != 404 {
			t.Fatalf("text file %s: %d", path, r.StatusCode)
		}
	}

	e.must(204, "DELETE", "/api/files/"+id, nil)
	if _, err := e.blobs.Get(context.Background(), o.S3Key.ValueOrZero()+".thumb"); err == nil {
		t.Fatal("thumbnail left behind after delete")
	}
}

// fund credits the signed-in user $5, as a confirmed payment would.
func (e *env) fund() {
	e.t.Helper()
	plan := billing.Plan{ID: "test", PriceMicros: 5 * billing.MicrosPerUSD, CreditMicros: 5 * billing.MicrosPerUSD}
	if err := e.srv.Ledger.Subscribe(context.Background(), e.userID(e.t), plan, "test"); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) userID(t *testing.T) uuid.UUID {
	return mustUUID(t, e.must(200, "GET", "/api/me", nil)["user"].(map[string]any)["id"].(string))
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAutoTitle(t *testing.T) {
	e := newEnv(t)
	e.signup("title@example.com")
	e.fund()
	if on := e.must(200, "GET", "/api/settings", nil)["auto_title"]; on != true {
		t.Fatalf("auto_title should default on, got %v", on)
	}
	e.llm.title = "<think>hm</think>\n\"Launch Codes.\""

	chatID := e.must(201, "POST", "/api/chats", nil)["chat"].(map[string]any)["id"].(string)
	before := e.available()
	_, ev := e.sendMessage(chatID, "what is the secret launch code")
	var titled string
	for _, x := range ev {
		if x.Name == "title" {
			titled = x.Data["title"].(string)
		}
	}
	if titled != "Launch Codes" || last(ev).Name != "done" {
		t.Fatalf("title event %q, events %v", titled, ev)
	}
	// The reply (1000 + 200) and the title (100 + 20) are both billed, each
	// rounded up to a whole token.
	if spent := before - e.available(); spent != billing.RoundMicros(1200)+billing.RoundMicros(120) {
		t.Fatalf("charged %d", spent)
	}
	chat := e.must(200, "GET", "/api/chats/"+chatID, nil)["chat"].(map[string]any)
	if chat["title"] != "Launch Codes" {
		t.Fatalf("title not stored: %v", chat)
	}

	// Renaming keeps the chat's place in the list; a new message moves it up.
	renamed := e.must(200, "PATCH", "/api/chats/"+chatID, map[string]string{"title": "mine"})["chat"].(map[string]any)
	if renamed["updated_at"] != chat["updated_at"] {
		t.Fatalf("rename bumped the chat: %v -> %v", chat["updated_at"], renamed["updated_at"])
	}
	e.sendMessage(chatID, "again")
	after := e.must(200, "GET", "/api/chats/"+chatID, nil)["chat"].(map[string]any)
	if after["title"] != "mine" || after["updated_at"] == chat["updated_at"] {
		t.Fatalf("second message: %v", after)
	}

	// Switched off, a new chat keeps its first words.
	if on := e.must(200, "PUT", "/api/settings", map[string]any{"auto_title": false})["auto_title"]; on != false {
		t.Fatalf("auto_title not saved: %v", on)
	}
	chatID = e.must(201, "POST", "/api/chats", nil)["chat"].(map[string]any)["id"].(string)
	e.sendMessage(chatID, "plain title please")
	if got := e.must(200, "GET", "/api/chats/"+chatID, nil)["chat"].(map[string]any)["title"]; got != "plain title please" {
		t.Fatalf("title with auto_title off: %v", got)
	}
}

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"Launch Codes":                     "Launch Codes",
		"Title: **Rust lifetimes**.":       "Rust lifetimes",
		"\n\n“Paris trip”\nextra":          "Paris trip",
		"<think>long thought</think>Taxes": "Taxes",
		"<think>never closed":              "",
		"":                                 "",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakePython hands back each input upper-cased as out-<name>, and a chart
// when the code is "chart".
type fakePython struct {
	mu sync.Mutex
	in []tools.File
}

func (f *fakePython) Run(_ context.Context, code string, in []tools.File) (tools.PythonResult, error) {
	f.mu.Lock()
	f.in = in
	f.mu.Unlock()
	res := tools.PythonResult{Stdout: "ran"}
	for _, x := range in {
		res.Files = append(res.Files, tools.File{Name: "out-" + x.Name, Data: bytes.ToUpper(x.Data)})
	}
	if code == "chart" {
		var buf bytes.Buffer
		pngenc.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 30, 20)))
		res.Files = append(res.Files, tools.File{Name: "chart.png", Data: buf.Bytes()})
	}
	return res, nil
}
func (f *fakePython) About() string               { return "" }
func (f *fakePython) Close(context.Context) error { return nil }
func (f *fakePython) inputs() []tools.File        { f.mu.Lock(); defer f.mu.Unlock(); return f.in }

func TestPythonFilesInAndOut(t *testing.T) {
	py := &fakePython{}
	e := newEnvWith(t, py)
	e.signup("py@example.com")
	e.fund()
	chatID := e.must(201, "POST", "/api/chats", map[string]string{"model": "hands"})["chat"].(map[string]any)["id"].(string)

	// A binary file can be attached when python can open it; the model is
	// told it's there rather than shown it.
	bin := e.upload("data.bin", []byte("secret payload\x00"))
	status, ev := e.sendMessage(chatID, `py {"code":"chart","files":["data.bin"]}`, bin["id"].(string))
	if status != 200 || last(ev).Name != "done" {
		t.Fatalf("send: %d %v", status, ev)
	}
	msgs := e.llm.lastMsgs
	if !strings.Contains(msgs[0].Content, "data.bin") {
		t.Fatalf("system prompt doesn't list the chat's files: %q", msgs[0].Content)
	}
	if in := py.inputs(); len(in) != 1 || in[0].Name != "data.bin" || string(in[0].Data) != "secret payload\x00" {
		t.Fatalf("python inputs: %v; reply %v", in, last(ev).Data)
	}

	// Outputs are saved as the user's files and listed on the step.
	step := last(ev).Data["message"].(map[string]any)["steps"].([]any)[0].(map[string]any)
	files, _ := step["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("step files: %v", step)
	}
	out, chart := files[0].(map[string]any), files[1].(map[string]any)
	if out["name"] != "out-data.bin" || chart["name"] != "chart.png" || chart["image"] != true {
		t.Fatalf("step files: %v", files)
	}
	resp, _ := e.do("GET", "/api/files/"+out["id"].(string)+"/content", nil)
	if raw, _ := io.ReadAll(resp.Body); string(raw) != "SECRET PAYLOAD\x00" {
		t.Fatalf("saved file: %q", raw)
	}
	if resp, _ := e.do("GET", "/api/files/"+chart["id"].(string)+"/thumbnail", nil); resp.StatusCode != 200 {
		t.Fatalf("chart thumbnail: %d", resp.StatusCode)
	}
	if n := len(e.must(200, "GET", "/api/files", nil)["files"].([]any)); n != 3 {
		t.Fatalf("want the upload and two saved files, got %d", n)
	}
	for _, v := range e.blobs.Keys() {
		if bytes.Contains(v, []byte("PAYLOAD")) {
			t.Fatal("plaintext in blob store")
		}
	}

	// A later message can open what an earlier run made.
	e.sendMessage(chatID, `py {"code":"x","files":["chart.png"]}`)
	if in := py.inputs(); len(in) != 1 || in[0].Name != "chart.png" || !bytes.HasPrefix(in[0].Data, []byte("\x89PNG")) {
		t.Fatalf("python inputs on the second turn: %v", in)
	}
	// An unknown name comes back to the model as an error listing the files.
	_, ev = e.sendMessage(chatID, `py {"code":"x","files":["nope.txt"]}`)
	reply := last(ev).Data["message"].(map[string]any)["content"].(string)
	if !strings.Contains(reply, "nope.txt") || !strings.Contains(reply, "chart.png") {
		t.Fatalf("reply: %q", reply)
	}
}

func lastLine(s string) string { return s[strings.LastIndex(s, "\n")+1:] }

func TestCryptoDisabledByDefault(t *testing.T) {
	e := newEnv(t)
	e.signup("crypto@example.com")
	out := e.must(200, "GET", "/api/billing/crypto", nil)
	if out["enabled"] != false {
		t.Fatalf("enabled = %v", out["enabled"])
	}
	e.must(404, "POST", "/api/billing/crypto/quote", map[string]any{"usd_micros": 10_000_000})
}

package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/reaganiwadha/adytus/apps/api/internal/crypto"
	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
	"github.com/reaganiwadha/adytus/apps/api/internal/imageprep"
	"github.com/reaganiwadha/adytus/apps/api/internal/llm"
	"github.com/reaganiwadha/adytus/apps/api/internal/meter"
	"github.com/reaganiwadha/adytus/apps/api/internal/store"
	"github.com/reaganiwadha/adytus/apps/api/internal/tools"
	"github.com/reaganiwadha/adytus/apps/api/internal/vault"
)

// Encrypted payloads.

type chatMeta struct {
	Title string `json:"title"`
	Model string `json:"model"`
}

type attachment struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Image bool      `json:"image,omitempty"`
	// Python marks a file the model doesn't read itself, only through the
	// tools that open files: too large, not text, or an image it can't see.
	Python bool `json:"python,omitempty"`
}

type messageUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	// CostMicros is only serialized for backoffice users; zero omits it.
	CostMicros int64 `json:"cost_micros,omitempty"`
}

// withoutMessageCost returns a copy that leaves the charge out of the API view
// while keeping the stored message intact.
func withoutMessageCost(b messageBody) messageBody {
	if b.Usage == nil {
		return b
	}
	u := *b.Usage
	u.CostMicros = 0
	b.Usage = &u
	return b
}

func withoutMessageCosts(msgs []messageView) {
	for i := range msgs {
		msgs[i].messageBody = withoutMessageCost(msgs[i].messageBody)
	}
}

type messageBody struct {
	Role        string        `json:"role"`
	Content     string        `json:"content"`
	Attachments []attachment  `json:"attachments,omitempty"`
	Model       string        `json:"model,omitempty"`
	Usage       *messageUsage `json:"usage,omitempty"`
	// Steps are the tool calls the model made on the way to this reply.
	Steps []tools.Step `json:"steps,omitempty"`
	// Partial marks a reply cut short by an error or a disconnect.
	Partial bool `json:"partial,omitempty"`
}

// API views.

type chatView struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	Model     string    `json:"model"`
	Offline   bool      `json:"offline"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type messageView struct {
	ID uuid.UUID `json:"id"`
	messageBody
	CreatedAt time.Time `json:"created_at"`
}

func viewChat(o *store.Object, m chatMeta) chatView {
	return chatView{o.ID, m.Title, m.Model, o.ServerWrappedDEK != nil, o.CreatedAt, o.UpdatedAt}
}

func paramID(c fiber.Ctx) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return uuid.Nil, errs.NotFound.New("not found")
	}
	return id, nil
}

func (s *Server) loadChat(c fiber.Ctx) (*store.Object, chatMeta, error) {
	var meta chatMeta
	id, err := paramID(c)
	if err != nil {
		return nil, meta, err
	}
	a := mustAuth(c)
	o, err := s.Store.GetObject(c.Context(), a.UserID, id, store.KindChat)
	if err != nil {
		return nil, meta, err
	}
	err = vault.Open(a.MK, o, &meta)
	return o, meta, err
}

func (s *Server) listChats(c fiber.Ctx) error {
	a := mustAuth(c)
	objs, err := s.Store.ListObjects(c.Context(), a.UserID, store.KindChat)
	if err != nil {
		return err
	}
	out := make([]chatView, 0, len(objs))
	for _, o := range objs {
		var m chatMeta
		if err := vault.Open(a.MK, o, &m); err != nil {
			return err
		}
		out = append(out, viewChat(o, m))
	}
	return c.JSON(fiber.Map{"chats": out})
}

func (s *Server) modelOrDefault(id string) (llm.Model, error) {
	if id == "" {
		return s.LLM.Current().Catalog.Default(), nil
	}
	m, ok := s.LLM.Current().Catalog.Get(id)
	if !ok {
		return m, errs.Validation.New("model %q is not available", id)
	}
	return m, nil
}

func (s *Server) createChat(c fiber.Ctx) error {
	var req struct {
		Title string `json:"title" validate:"max=200"`
		Model string `json:"model"`
	}
	if len(c.Body()) > 0 {
		if err := c.Bind().Body(&req); err != nil {
			return err
		}
	}
	model, err := s.modelOrDefault(req.Model)
	if err != nil {
		return err
	}
	a := mustAuth(c)
	o, dek, err := vault.New(a.MK, nil, a.UserID, store.KindChat, uuid.NullUUID{})
	if err != nil {
		return err
	}
	defer crypto.Zero(dek)
	meta := chatMeta{Title: req.Title, Model: model.ID}
	if o.Ciphertext, err = vault.Seal(dek, o.ID, meta); err != nil {
		return err
	}
	if err := s.Store.InsertObject(c.Context(), o); err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"chat": viewChat(o, meta)})
}

func (s *Server) loadMessages(ctx context.Context, a *authCtx, chatID uuid.UUID) ([]messageView, error) {
	objs, err := s.Store.Children(ctx, a.UserID, chatID)
	if err != nil {
		return nil, err
	}
	out := make([]messageView, 0, len(objs))
	for _, o := range objs {
		var b messageBody
		if err := vault.Open(a.MK, o, &b); err != nil {
			return nil, err
		}
		out = append(out, messageView{o.ID, b, o.CreatedAt})
	}
	return out, nil
}

func (s *Server) getChat(c fiber.Ctx) error {
	o, meta, err := s.loadChat(c)
	if err != nil {
		return err
	}
	msgs, err := s.loadMessages(c.Context(), mustAuth(c), o.ID)
	if err != nil {
		return err
	}
	if !s.isBackoffice(c) {
		withoutMessageCosts(msgs)
	}
	return c.JSON(fiber.Map{"chat": viewChat(o, meta), "messages": msgs})
}

func (s *Server) updateChat(c fiber.Ctx) error {
	var req struct {
		Title *string `json:"title" validate:"omitempty,max=200"`
		Model *string `json:"model"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return err
	}
	o, meta, err := s.loadChat(c)
	if err != nil {
		return err
	}
	if req.Title != nil {
		meta.Title = *req.Title
	}
	if req.Model != nil {
		m, err := s.modelOrDefault(*req.Model)
		if err != nil {
			return err
		}
		meta.Model = m.ID
	}
	dek, err := vault.DEK(mustAuth(c).MK, o)
	if err != nil {
		return err
	}
	defer crypto.Zero(dek)
	if o.Ciphertext, err = vault.Seal(dek, o.ID, meta); err != nil {
		return err
	}
	if err := s.Store.UpdateCiphertext(c.Context(), o.ID, o.Ciphertext); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"chat": viewChat(o, meta)})
}

func (s *Server) deleteChat(c fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return err
	}
	if _, err := s.Store.DeleteObject(c.Context(), mustAuth(c).UserID, id, store.KindChat); err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}

type offlineReq struct {
	Enabled bool `json:"enabled"`
}

// setOffline opts an object and its children in or out of offline
// processing by adding or removing the server-wrapped copy of each DEK.
func (s *Server) setOffline(c fiber.Ctx, root *store.Object, enabled bool) error {
	a := mustAuth(c)
	objs := []*store.Object{root}
	children, err := s.Store.Children(c.Context(), a.UserID, root.ID)
	if err != nil {
		return err
	}
	objs = append(objs, children...)

	return s.Store.Tx(c.Context(), func(tx pgx.Tx) error {
		for _, o := range objs {
			var wrapped []byte
			if enabled {
				if wrapped, err = vault.WrapForServer(a.MK, s.Config.ServerKey, o); err != nil {
					return err
				}
			}
			if err := store.SetServerWrappedDEK(c.Context(), tx, o.ID, wrapped); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Server) setChatOffline(c fiber.Ctx) error {
	var req offlineReq
	if err := c.Bind().Body(&req); err != nil {
		return err
	}
	o, meta, err := s.loadChat(c)
	if err != nil {
		return err
	}
	if err := s.setOffline(c, o, req.Enabled); err != nil {
		return err
	}
	view := viewChat(o, meta)
	view.Offline = req.Enabled
	return c.JSON(fiber.Map{"chat": view})
}

// --- sending a message ---

const (
	maxAttachmentBytes = 200 * 1024
	maxImageBytes      = 20 << 20
)

type sendReq struct {
	Content string      `json:"content" validate:"required,max=100000"`
	FileIDs []uuid.UUID `json:"file_ids" validate:"max=5"`
}

func titleFrom(content string) string {
	t := strings.Join(strings.Fields(content), " ")
	if utf8.RuneCountInString(t) > 60 {
		t = string([]rune(t)[:60]) + "…"
	}
	return t
}

// attachments loads attached files for the model once per request: text is
// inlined, images are downscaled and re-encoded as JPEG (base64).
type attachments struct {
	s      *Server
	ctx    context.Context
	a      *authCtx
	vision bool
	text   map[uuid.UUID]string
	images map[uuid.UUID]string
}

func (at *attachments) load(att attachment) {
	if _, ok := at.text[att.ID]; ok {
		return
	}
	if _, ok := at.images[att.ID]; ok {
		return
	}
	_, data, err := at.s.readFile(at.ctx, at.a, att.ID)
	switch {
	case err != nil:
		at.text[att.ID] = "[file no longer available]"
	case att.Image:
		if !at.vision {
			at.text[att.ID] = "[image: this model can't see images]"
		} else if jpg, err := imageprep.Prepare(data); err != nil {
			at.text[att.ID] = "[image could not be read]"
		} else {
			at.images[att.ID] = base64.StdEncoding.EncodeToString(jpg)
		}
	default:
		at.text[att.ID] = string(data)
	}
}

// message builds the model's view of a stored message.
func (at *attachments) message(b messageBody) *schema.Message {
	role := schema.User
	if b.Role == "assistant" {
		role = schema.Assistant
	}
	if len(b.Attachments) == 0 {
		return &schema.Message{Role: role, Content: b.Content}
	}
	var sb strings.Builder
	var imgs []schema.MessageInputPart
	for _, att := range b.Attachments {
		if att.Python {
			fmt.Fprintf(&sb, "<file name=%q>[not shown to you; open it with your tools]</file>\n\n", att.Name)
			continue
		}
		at.load(att)
		if data, ok := at.images[att.ID]; ok {
			imgs = append(imgs, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &data, MIMEType: "image/jpeg",
				}},
			})
			continue
		}
		fmt.Fprintf(&sb, "<file name=%q>\n%s\n</file>\n\n", att.Name, at.text[att.ID])
	}
	sb.WriteString(b.Content)
	if len(imgs) == 0 {
		return &schema.Message{Role: role, Content: sb.String()}
	}
	parts := append([]schema.MessageInputPart{{Type: schema.ChatMessagePartTypeText, Text: sb.String()}}, imgs...)
	return &schema.Message{Role: role, UserInputMultiContent: parts}
}

func (s *Server) sendMessage(c fiber.Ctx) error {
	var req sendReq
	if err := c.Bind().Body(&req); err != nil {
		return err
	}
	a := mustAuth(c)
	ctx := c.Context()

	chat, meta, err := s.loadChat(c)
	if err != nil {
		return err
	}
	// One snapshot for the whole reply, so a backoffice change mid-stream
	// doesn't switch providers under it.
	snap := s.LLM.Current()
	spec, ok := snap.Catalog.Get(meta.Model)
	if !ok {
		return errs.Validation.New("this chat's model is no longer available; pick another")
	}

	settings, err := s.Store.Settings(ctx, a.UserID)
	if err != nil {
		return err
	}
	// Tools are offered when the model can call them and the user left
	// some switched on.
	var toolIDs []string
	if spec.Tools && s.Tools != nil {
		toolIDs = s.Tools.Enabled(settings.Tools)
	}
	opensFiles := slices.ContainsFunc(toolIDs, tools.OpensFiles)

	// Attachments must be the user's files: text small enough to inline,
	// images when the model has vision, or anything when a tool can open it.
	userMsg := messageBody{Role: "user", Content: req.Content}
	at := &attachments{s: s, ctx: ctx, a: a, vision: spec.Vision, text: map[uuid.UUID]string{}, images: map[uuid.UUID]string{}}
	total := 0
	for _, id := range req.FileIDs {
		fm, data, err := s.readFile(ctx, a, id)
		if err != nil {
			return err
		}
		att := attachment{ID: id, Name: fm.Name, Image: fm.Image}
		switch {
		case fm.Image && spec.Vision && len(data) <= maxImageBytes:
			jpg, err := imageprep.Prepare(data)
			if err != nil {
				return errs.Validation.New("%s: %s", fm.Name, err.Error())
			}
			at.images[id] = base64.StdEncoding.EncodeToString(jpg)
		case fm.Text && total+len(data) <= maxAttachmentBytes:
			total += len(data)
			at.text[id] = string(data)
		case opensFiles:
			att.Python = true
		case fm.Image && !spec.Vision:
			return errs.Validation.New("%s can't see images; switch to a vision model to attach %s", spec.Name, fm.Name)
		case fm.Image:
			return errs.Validation.New("%s is too large to attach (max %d MB)", fm.Name, maxImageBytes>>20)
		case fm.Text:
			return errs.Validation.New("attachments are too large (max %d KB of text in total)", maxAttachmentBytes/1024)
		default:
			return errs.Validation.New("%s can't be attached; only text files and images can, or any file when the document reader or Python tool is on", fm.Name)
		}
		userMsg.Attachments = append(userMsg.Attachments, att)
	}

	history, err := s.loadMessages(ctx, a, chat.ID)
	if err != nil {
		return err
	}
	// The file tools can open the chat's files: everything attached and
	// everything tools saved, newest first.
	var ws *workspace
	system := s.Config.SystemPrompt
	if opensFiles {
		var ids []uuid.UUID
		for _, m := range append(history, messageView{messageBody: userMsg}) {
			for _, att := range m.Attachments {
				ids = append(ids, att.ID)
			}
			for _, st := range m.Steps {
				for _, f := range st.Files {
					if id, err := uuid.Parse(f.ID); err == nil {
						ids = append(ids, id)
					}
				}
			}
		}
		slices.Reverse(ids)
		if ws, err = s.newWorkspace(ctx, a, ids); err != nil {
			return err
		}
		system += ws.prompt()
	}
	// Until the stream takes it over, the workspace is closed on any error.
	streaming := false
	defer func() {
		if ws != nil && !streaming {
			ws.close()
		}
	}()

	prompt := []*schema.Message{schema.SystemMessage(system)}
	for _, m := range history {
		if m.Content == "" {
			continue
		}
		prompt = append(prompt, at.message(m.messageBody))
	}
	prompt = append(prompt, at.message(userMsg))

	prompt, _, err = llm.FitContext(spec, prompt)
	if err != nil {
		return errs.Validation.New("%s", err.Error())
	}

	// Hold the worst case for the first call (estimated prompt, tool
	// definitions and a full-length reply) now, so a lack of credit is a
	// plain 402.
	chatModel := snap.ChatModel(spec)
	mm := meter.New(chatModel, s.Ledger, a.UserID, spec)
	if len(toolIDs) > 0 {
		infos, err := s.Tools.ToolInfos(ctx, toolIDs)
		if err != nil {
			return err
		}
		withTools, err := mm.WithTools(infos)
		if err != nil {
			return err
		}
		// The tool-bound copy shares the meter's state, so the hold and every
		// call in the loop settle on the same totals.
		mm = withTools.(*meter.Model)
	}
	holdAmount, err := mm.Reserve(ctx, prompt)
	if err != nil {
		return err
	}

	// Prepare everything that needs the master key now: the stream writer
	// runs after this handler returns, when the request's MK is zeroed.
	prepared, err := s.prepareReply(a, chat, &meta, userMsg, req.Content, settings.AutoTitleOn())
	if err != nil {
		mm.ReleasePending(ctx)
		return err
	}

	userID := a.UserID
	backoffice := s.isBackoffice(c)
	streaming = true
	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("X-Accel-Buffering", "no")

	return c.SendStreamWriter(func(w *bufio.Writer) {
		defer crypto.Zero(prepared.replyDEK)
		defer crypto.Zero(prepared.chatDEK)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if ws != nil {
			defer ws.close()
			ctx = tools.WithWorkspace(ctx, ws)
		}

		// Tool steps arrive from the tools node, possibly in parallel.
		var mu sync.Mutex
		clientGone := false
		send := func(event string, v any) error {
			mu.Lock()
			defer mu.Unlock()
			if clientGone {
				return errClientGone
			}
			if err := writeEvent(w, event, v); err != nil {
				clientGone = true
				return errClientGone
			}
			return nil
		}

		_ = send("start", fiber.Map{"user_message_id": prepared.userObj.ID, "message_id": prepared.replyObj.ID, "title": meta.Title, "hold_micros": holdAmount})

		// A new chat is named by the model while the reply streams; the
		// message's first words stand in until then.
		titled := make(chan string, 1)
		if prepared.chatDEK != nil {
			titleCtx, cancelTitle := context.WithTimeout(ctx, 30*time.Second)
			defer cancelTitle()
			go func() {
				t := s.generateTitle(titleCtx, chatModel, userID, spec, req.Content)
				if t != "" {
					_ = send("title", fiber.Map{"title": t})
				}
				titled <- t
			}()
		}

		var content strings.Builder
		var steps []tools.Step
		observe := func(st tools.Step) {
			mu.Lock()
			if st.Done {
				for i := range steps {
					if steps[i].ID == st.ID {
						steps[i] = st
					}
				}
			} else {
				steps = append(steps, st)
			}
			mu.Unlock()
			_ = send("tool", st)
		}

		var streamErr error
		sr, err := s.reply(ctx, mm, prompt, toolIDs, observe)
		if err != nil {
			streamErr = err
		} else {
			for {
				msg, err := sr.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					streamErr = err
					break
				}
				if msg.ReasoningContent != "" {
					if send("reasoning", fiber.Map{"text": msg.ReasoningContent}) != nil {
						streamErr = errClientGone
						break
					}
				}
				if msg.Content == "" {
					continue
				}
				content.WriteString(msg.Content)
				if send("delta", fiber.Map{"text": msg.Content}) != nil {
					streamErr = errClientGone
					break
				}
			}
			sr.Close()
		}
		mm.ReleasePending(ctx)
		mm.Wait()

		usage := mm.Usage()
		if usage.Calls == 0 {
			slog.Warn("llm request failed", "err", streamErr)
			_ = send("error", fiber.Map{"error": "upstream", "message": "the model did not respond; nothing was charged"})
			return
		}

		reply := messageBody{
			Role: "assistant", Content: content.String(), Model: spec.ID, Partial: streamErr != nil,
			Usage: &messageUsage{PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, CostMicros: usage.CostMicros},
			Steps: steps,
		}
		viewReply := reply
		if !backoffice {
			viewReply = withoutMessageCost(reply)
		}
		if prepared.chatDEK != nil {
			if t := <-titled; t != "" {
				named := prepared.meta
				named.Title = t
				if ct, err := vault.Seal(prepared.chatDEK, chat.ID, named); err == nil {
					prepared.chatCiphertext = ct
				}
			}
		}
		if err := s.storeReply(ctx, chat.ID, prepared, reply); err != nil {
			slog.Error("store messages", "err", err, "user", userID)
			_ = send("error", fiber.Map{"error": "internal", "message": "reply could not be saved"})
			return
		}

		if streamErr != nil && !clientGone {
			slog.Warn("llm stream ended with error", "err", streamErr)
		}
		balance, _ := s.Ledger.Balance(ctx, userID)
		_ = send("done", fiber.Map{
			"message":      messageView{prepared.replyObj.ID, viewReply, prepared.replyObj.CreatedAt},
			"user_message": messageView{prepared.userObj.ID, userMsg, prepared.userObj.CreatedAt},
			"balance":      balance,
		})
	})
}

// reply streams the model's answer. With tools it runs the loop itself: the
// model may call tools any number of times (each call metered and billed)
// before its final answer. Text the model writes before a tool call streams
// like any other delta, so a model that narrates first ("I'll open the files")
// still gets its calls run. Eino's ReAct agent could not: its default stream
// checker treats the first text chunk as proof that the reply is finished and
// drops the tool call that follows it.
func (s *Server) reply(ctx context.Context, mm model.ToolCallingChatModel, prompt []*schema.Message, toolIDs []string, observe tools.Observer) (*schema.StreamReader[*schema.Message], error) {
	if len(toolIDs) == 0 {
		return mm.Stream(ctx, prompt)
	}
	byName := map[string]tool.InvokableTool{}
	for _, t := range s.Tools.Tools(toolIDs, observe) {
		info, err := t.Info(ctx)
		if err != nil {
			return nil, err
		}
		if invokable, ok := t.(tool.InvokableTool); ok {
			byName[info.Name] = invokable
		}
	}

	out, sw := schema.Pipe[*schema.Message](8)
	go func() {
		defer sw.Close()
		messages := append([]*schema.Message(nil), prompt...)
		for round := 0; round <= s.Config.ToolMaxSteps; round++ {
			sr, err := mm.Stream(ctx, messages)
			if err != nil {
				_ = sw.Send(nil, err)
				return
			}
			var chunks []*schema.Message
			for {
				msg, err := sr.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					sr.Close()
					_ = sw.Send(nil, err)
					return
				}
				chunks = append(chunks, msg)
				// Stream text as it arrives, including a preamble to a tool
				// call, so the user sees progress either way.
				if (msg.Content != "" || msg.ReasoningContent != "") && sw.Send(msg, nil) {
					sr.Close()
					return
				}
			}
			sr.Close()
			if len(chunks) == 0 {
				return
			}
			full, err := schema.ConcatMessages(chunks)
			if err != nil {
				_ = sw.Send(nil, err)
				return
			}
			if len(full.ToolCalls) == 0 {
				return
			}
			if round == s.Config.ToolMaxSteps {
				break
			}
			messages = append(messages, full)
			for _, call := range full.ToolCalls {
				result := fmt.Sprintf("error: no tool named %q", call.Function.Name)
				if invokable, ok := byName[call.Function.Name]; ok {
					out, runErr := invokable.InvokableRun(ctx, call.Function.Arguments)
					result = out
					if runErr != nil {
						// Errors go back to the model as text so it can recover.
						result = "error: " + runErr.Error()
					}
				}
				messages = append(messages, schema.ToolMessage(result, call.ID, schema.WithToolName(call.Function.Name)))
			}
		}
		_ = sw.Send(nil, fmt.Errorf("the model kept calling tools for more than %d rounds", s.Config.ToolMaxSteps))
	}()
	return out, nil
}

// preparedReply holds the sealed user message and the reply's object and key,
// made while the master key is still available.
type preparedReply struct {
	userObj, replyObj *store.Object
	replyDEK          []byte
	chatCiphertext    []byte // set when the chat gets its title
	// chatDEK and meta are kept when the model should name the chat, to
	// seal the title once it arrives.
	chatDEK []byte
	meta    chatMeta
}

func (s *Server) prepareReply(a *authCtx, chat *store.Object, meta *chatMeta, userMsg messageBody, content string, autoTitle bool) (*preparedReply, error) {
	var serverKey []byte
	if chat.ServerWrappedDEK != nil {
		serverKey = s.Config.ServerKey
	}
	parent := uuid.NullUUID{UUID: chat.ID, Valid: true}
	p := &preparedReply{}
	userObj, userDEK, err := vault.New(a.MK, serverKey, a.UserID, store.KindMessage, parent)
	if err != nil {
		return nil, err
	}
	userObj.Ciphertext, err = vault.Seal(userDEK, userObj.ID, userMsg)
	crypto.Zero(userDEK)
	if err != nil {
		return nil, err
	}
	p.userObj = userObj
	if p.replyObj, p.replyDEK, err = vault.New(a.MK, serverKey, a.UserID, store.KindMessage, parent); err != nil {
		return nil, err
	}
	if meta.Title == "" {
		meta.Title = titleFrom(content)
		chatDEK, err := vault.DEK(a.MK, chat)
		if err != nil {
			return nil, err
		}
		p.chatCiphertext, err = vault.Seal(chatDEK, chat.ID, meta)
		if err != nil || !autoTitle {
			crypto.Zero(chatDEK)
		}
		if err != nil {
			return nil, err
		}
		if autoTitle {
			p.chatDEK, p.meta = chatDEK, *meta
		}
	}
	return p, nil
}

func (s *Server) storeReply(ctx context.Context, chatID uuid.UUID, p *preparedReply, reply messageBody) error {
	var err error
	if p.replyObj.Ciphertext, err = vault.Seal(p.replyDEK, p.replyObj.ID, reply); err != nil {
		return err
	}
	return s.Store.Tx(ctx, func(tx pgx.Tx) error {
		if err := store.InsertObject(ctx, tx, p.userObj); err != nil {
			return err
		}
		if err := store.InsertObject(ctx, tx, p.replyObj); err != nil {
			return err
		}
		if p.chatCiphertext != nil {
			if err := store.UpdateCiphertext(ctx, tx, chatID, p.chatCiphertext); err != nil {
				return err
			}
		}
		return store.Touch(ctx, tx, chatID)
	})
}

// generateTitle asks the chat's model to name a chat that opens with
// content. The call is metered and billed like any other; on failure it
// returns "" and the chat keeps its stand-in title.
func (s *Server) generateTitle(ctx context.Context, chatModel model.ToolCallingChatModel, userID uuid.UUID, spec llm.Model, content string) string {
	if r := []rune(content); len(r) > 2000 {
		content = string(r[:2000])
	}
	mm := meter.New(chatModel, s.Ledger, userID, spec)
	out, err := mm.Generate(ctx, []*schema.Message{
		schema.SystemMessage("You name chats. Reply with a title of 2 to 6 words for a chat that opens with the user's message below, in the message's language. Reply with the title only: no quotes, no markdown, no final period."),
		schema.UserMessage(content),
	})
	if err != nil {
		slog.Warn("chat title", "err", err)
		return ""
	}
	return cleanTitle(out.Content)
}

const titleDecor = " \t\"'`*#_“”‘’"

var thinkBlock = regexp.MustCompile(`(?s)<think>.*?(</think>|$)`)

// cleanTitle takes the first line of a model's title and strips the
// decoration models add anyway.
func cleanTitle(s string) string {
	s = thinkBlock.ReplaceAllString(s, "")
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if len(line) >= 6 && strings.EqualFold(line[:6], "title:") {
			line = line[6:]
		}
		line = strings.Trim(line, titleDecor)
		line = strings.Trim(strings.TrimRight(line, ".。"), titleDecor)
		if line != "" {
			return titleFrom(line)
		}
	}
	return ""
}

var errClientGone = fmt.Errorf("client disconnected")

func writeEvent(w *bufio.Writer, event string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	return w.Flush()
}

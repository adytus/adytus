package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/reaganiwadha/adytus/apps/api/internal/billing"
	"github.com/reaganiwadha/adytus/apps/api/internal/crypto"
	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
	"github.com/reaganiwadha/adytus/apps/api/internal/llm"
	"github.com/reaganiwadha/adytus/apps/api/internal/store"
	"github.com/reaganiwadha/adytus/apps/api/internal/vault"
)

// Encrypted payloads.

type chatMeta struct {
	Title string `json:"title"`
	Model string `json:"model"`
}

type attachment struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type messageUsage struct {
	PromptTokens     int   `json:"prompt_tokens"`
	CompletionTokens int   `json:"completion_tokens"`
	CostMicros       int64 `json:"cost_micros"`
}

type messageBody struct {
	Role        string        `json:"role"`
	Content     string        `json:"content"`
	Attachments []attachment  `json:"attachments,omitempty"`
	Model       string        `json:"model,omitempty"`
	Usage       *messageUsage `json:"usage,omitempty"`
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
		return s.Catalog.Default(), nil
	}
	m, ok := s.Catalog.Get(id)
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

const maxAttachmentBytes = 200 * 1024

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

// promptContent inlines attached text files into a message for the model.
func (s *Server) promptContent(ctx context.Context, a *authCtx, b messageBody, cache map[uuid.UUID]string) string {
	if len(b.Attachments) == 0 {
		return b.Content
	}
	var sb strings.Builder
	for _, att := range b.Attachments {
		text, ok := cache[att.ID]
		if !ok {
			_, data, err := s.readFile(ctx, a, att.ID)
			if err != nil {
				text = "[file no longer available]"
			} else {
				text = string(data)
			}
			cache[att.ID] = text
		}
		fmt.Fprintf(&sb, "<file name=%q>\n%s\n</file>\n\n", att.Name, text)
	}
	sb.WriteString(b.Content)
	return sb.String()
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
	model, ok := s.Catalog.Get(meta.Model)
	if !ok {
		return errs.Validation.New("this chat's model is no longer available; pick another")
	}

	// Attachments must be the user's text files, small enough to inline.
	userMsg := messageBody{Role: "user", Content: req.Content}
	cache := map[uuid.UUID]string{}
	total := 0
	for _, id := range req.FileIDs {
		meta, data, err := s.readFile(ctx, a, id)
		if err != nil {
			return err
		}
		if !meta.Text {
			return errs.Validation.New("%s is not a text file; only text files can be attached", meta.Name)
		}
		if total += len(data); total > maxAttachmentBytes {
			return errs.Validation.New("attachments are too large (max %d KB in total)", maxAttachmentBytes/1024)
		}
		cache[id] = string(data)
		userMsg.Attachments = append(userMsg.Attachments, attachment{id, meta.Name})
	}

	history, err := s.loadMessages(ctx, a, chat.ID)
	if err != nil {
		return err
	}
	prompt := []llm.Message{{Role: "system", Content: s.Config.SystemPrompt}}
	for _, m := range history {
		if m.Content == "" {
			continue
		}
		prompt = append(prompt, llm.Message{Role: m.Role, Content: s.promptContent(ctx, a, m.messageBody, cache)})
	}
	prompt = append(prompt, llm.Message{Role: "user", Content: s.promptContent(ctx, a, userMsg, cache)})

	prompt, estPrompt, err := llm.FitContext(model, prompt)
	if err != nil {
		return errs.Validation.New("%s", err.Error())
	}

	// Hold the worst case: estimated prompt plus a full-length reply.
	holdAmount := model.Cost(estPrompt, model.MaxOutputTokens)
	holdID, err := s.Ledger.Reserve(ctx, a.UserID, holdAmount)
	if err != nil {
		return err
	}

	// Prepare everything that needs the master key now: the stream writer
	// runs after this handler returns, when the request's MK is zeroed.
	var serverKey []byte
	if chat.ServerWrappedDEK != nil {
		serverKey = s.Config.ServerKey
	}
	parent := uuid.NullUUID{UUID: chat.ID, Valid: true}
	userObj, userDEK, err := vault.New(a.MK, serverKey, a.UserID, store.KindMessage, parent)
	if err != nil {
		return err
	}
	userObj.Ciphertext, err = vault.Seal(userDEK, userObj.ID, userMsg)
	crypto.Zero(userDEK)
	if err != nil {
		return err
	}
	replyObj, replyDEK, err := vault.New(a.MK, serverKey, a.UserID, store.KindMessage, parent)
	if err != nil {
		return err
	}

	var newChatCiphertext []byte
	if meta.Title == "" {
		meta.Title = titleFrom(req.Content)
		chatDEK, err := vault.DEK(a.MK, chat)
		if err != nil {
			return err
		}
		newChatCiphertext, err = vault.Seal(chatDEK, chat.ID, meta)
		crypto.Zero(chatDEK)
		if err != nil {
			return err
		}
	}

	userID := a.UserID
	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("X-Accel-Buffering", "no")

	return c.SendStreamWriter(func(w *bufio.Writer) {
		defer crypto.Zero(replyDEK)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		clientGone := false
		send := func(event string, v any) error {
			if clientGone {
				return errClientGone
			}
			if err := writeEvent(w, event, v); err != nil {
				clientGone = true
				return errClientGone
			}
			return nil
		}

		_ = send("start", fiber.Map{"user_message_id": userObj.ID, "message_id": replyObj.ID, "title": meta.Title, "hold_micros": holdAmount})

		resp, streamErr := s.LLM.Stream(ctx, llm.ChatRequest{
			Model: model.Upstream, Messages: prompt, MaxTokens: model.MaxOutputTokens,
		}, func(delta string) error {
			return send("delta", fiber.Map{"text": delta})
		})

		// Nothing generated and nothing reported: the request never ran.
		if resp == nil || (resp.Content == "" && !resp.Usage.Reported) {
			if err := s.Ledger.Release(ctx, holdID); err != nil {
				slog.Error("release hold", "err", err)
			}
			slog.Warn("llm request failed", "err", streamErr)
			_ = send("error", fiber.Map{"error": "upstream", "message": "the model did not respond; nothing was charged"})
			return
		}

		usage := resp.Usage
		if !usage.Reported {
			// Cancelled mid-stream: bill our pessimistic estimate.
			usage.PromptTokens = estPrompt
			usage.CompletionTokens = llm.EstimateTokens(resp.Content)
		}
		cost := model.ChargeFor(usage)
		if err := s.Ledger.Settle(ctx, userID, holdID, billing.Usage{
			Model: model.ID, PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, CostMicros: cost,
		}); err != nil {
			slog.Error("settle usage", "err", err, "user", userID)
		}

		reply := messageBody{
			Role: "assistant", Content: resp.Content, Model: model.ID, Partial: streamErr != nil,
			Usage: &messageUsage{usage.PromptTokens, usage.CompletionTokens, cost},
		}
		var err error
		if replyObj.Ciphertext, err = vault.Seal(replyDEK, replyObj.ID, reply); err == nil {
			err = s.Store.Tx(ctx, func(tx pgx.Tx) error {
				if err := store.InsertObject(ctx, tx, userObj); err != nil {
					return err
				}
				if err := store.InsertObject(ctx, tx, replyObj); err != nil {
					return err
				}
				if newChatCiphertext != nil {
					return store.UpdateCiphertext(ctx, tx, chat.ID, newChatCiphertext)
				}
				return store.Touch(ctx, tx, chat.ID)
			})
		}
		if err != nil {
			slog.Error("store messages", "err", err, "user", userID)
			_ = send("error", fiber.Map{"error": "internal", "message": "reply could not be saved"})
			return
		}

		if streamErr != nil && !clientGone {
			slog.Warn("llm stream ended with error", "err", streamErr)
		}
		balance, _ := s.Ledger.Balance(ctx, userID)
		_ = send("done", fiber.Map{
			"message":      messageView{replyObj.ID, reply, replyObj.CreatedAt},
			"user_message": messageView{userObj.ID, userMsg, userObj.CreatedAt},
			"balance":      balance,
		})
	})
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

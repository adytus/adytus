package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/guregu/null/v6"

	"github.com/reaganiwadha/adytus/apps/api/internal/crypto"
	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
	"github.com/reaganiwadha/adytus/apps/api/internal/imageprep"
	"github.com/reaganiwadha/adytus/apps/api/internal/store"
	"github.com/reaganiwadha/adytus/apps/api/internal/vault"
)

// fileMeta is stored encrypted in the object row; the S3 object holds only
// the encrypted content under an opaque key.
type fileMeta struct {
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
	// Text files can be attached to chat messages, and images too when the
	// chat's model has vision.
	Text  bool `json:"text"`
	Image bool `json:"image"`
	// Thumb is set when an encrypted preview is stored beside the file.
	Thumb bool `json:"thumb"`
}

type fileView struct {
	ID uuid.UUID `json:"id"`
	fileMeta
	Offline   bool      `json:"offline"`
	CreatedAt time.Time `json:"created_at"`
}

func viewFile(o *store.Object, m fileMeta) fileView {
	return fileView{o.ID, m, o.ServerWrappedDEK != nil, o.CreatedAt}
}

func isText(data []byte) bool {
	return utf8.Valid(data) && !bytes.ContainsRune(data, 0)
}

func (s *Server) listFiles(c fiber.Ctx) error {
	a := mustAuth(c)
	objs, err := s.Store.ListObjects(c.Context(), a.UserID, store.KindFile)
	if err != nil {
		return err
	}
	out := make([]fileView, 0, len(objs))
	for _, o := range objs {
		var m fileMeta
		if err := vault.Open(a.MK, o, &m); err != nil {
			return err
		}
		out = append(out, viewFile(o, m))
	}
	return c.JSON(fiber.Map{"files": out})
}

func (s *Server) uploadFile(c fiber.Ctx) error {
	fh, err := c.FormFile("file")
	if err != nil {
		return errs.Validation.New("expected a multipart field named \"file\"")
	}
	if fh.Size > int64(s.Config.MaxUploadBytes) {
		return errs.Validation.New("file is larger than %d MB", s.Config.MaxUploadBytes>>20)
	}
	f, err := fh.Open()
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(s.Config.MaxUploadBytes)+1))
	f.Close()
	if err != nil {
		return err
	}

	name := filepath.Base(fh.Filename)
	if len(name) > 255 {
		name = name[:255]
	}
	mt := fh.Header.Get("Content-Type")
	if mt == "" {
		mt = mime.TypeByExtension(filepath.Ext(name))
	}
	a := mustAuth(c)
	o, dek, err := vault.New(a.MK, nil, a.UserID, store.KindFile, uuid.NullUUID{})
	if err != nil {
		return err
	}
	defer crypto.Zero(dek)
	meta, err := s.storeFile(c.Context(), o, dek, name, mt, data)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"file": viewFile(o, meta)})
}

// storeFile encrypts data into the new file object o and stores it, with a
// preview for images.
func (s *Server) storeFile(ctx context.Context, o *store.Object, dek []byte, name, mt string, data []byte) (fileMeta, error) {
	meta := fileMeta{Name: name, Mime: mt, Size: int64(len(data)), Text: isText(data), Image: imageprep.Detect(data)}
	sealed, err := vault.SealBytes(dek, o.ID, data)
	if err != nil {
		return meta, err
	}
	key := "u/" + o.UserID.String() + "/" + o.ID.String()
	if err := s.Blobs.Put(ctx, key, sealed); err != nil {
		return meta, errs.Upstream.Wrap(err, "storage unavailable")
	}
	o.S3Key = null.StringFrom(key)

	// Image previews are made now, while the plaintext is in hand, and
	// sealed with the file's own key.
	if meta.Image {
		if thumb, err := imageprep.Thumbnail(data); err == nil {
			sealedThumb, err := vault.SealPart(dek, o.ID, "thumb", thumb)
			if err == nil {
				err = s.Blobs.Put(ctx, thumbKey(key), sealedThumb)
			}
			if err != nil {
				slog.Warn("store thumbnail", "err", err)
			} else {
				meta.Thumb = true
			}
		}
	}
	if o.Ciphertext, err = vault.Seal(dek, o.ID, meta); err != nil {
		return meta, err
	}
	if err := s.Store.InsertObject(ctx, o); err != nil {
		_ = s.Blobs.Delete(context.Background(), key)
		if meta.Thumb {
			_ = s.Blobs.Delete(context.Background(), thumbKey(key))
		}
		return meta, err
	}
	return meta, nil
}

// readFile loads and decrypts one of the user's files.
func (s *Server) readFile(ctx context.Context, a *authCtx, id uuid.UUID) (fileMeta, []byte, error) {
	var meta fileMeta
	o, err := s.Store.GetObject(ctx, a.UserID, id, store.KindFile)
	if err != nil {
		return meta, nil, err
	}
	dek, err := vault.DEK(a.MK, o)
	if err != nil {
		return meta, nil, err
	}
	defer crypto.Zero(dek)
	if err := vault.OpenWithDEK(dek, o, &meta); err != nil {
		return meta, nil, err
	}
	sealed, err := s.Blobs.Get(ctx, o.S3Key.ValueOrZero())
	if err != nil {
		return meta, nil, errs.Upstream.Wrap(err, "storage unavailable")
	}
	data, err := vault.OpenBytes(dek, o.ID, sealed)
	return meta, data, err
}

func (s *Server) downloadFile(c fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return err
	}
	meta, data, err := s.readFile(c.Context(), mustAuth(c), id)
	if err != nil {
		return err
	}
	// Always download, never render: uploaded HTML must not run on our origin.
	c.Set("Content-Type", "application/octet-stream")
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": meta.Name}))
	c.Set("Cache-Control", "private, no-store")
	return c.Send(data)
}

// viewImage serves an image file re-encoded as JPEG for inline display.
// Re-encoding means only pixels reach the page, never the uploaded bytes.
func (s *Server) viewImage(c fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return err
	}
	meta, data, err := s.readFile(c.Context(), mustAuth(c), id)
	if err != nil {
		return err
	}
	if !meta.Image {
		return errs.NotFound.New("%s is not an image", meta.Name)
	}
	jpg, err := imageprep.View(data)
	if err != nil {
		return errs.Validation.New("%s: %s", meta.Name, err.Error())
	}
	c.Set("Content-Type", "image/jpeg")
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("Cache-Control", "private, max-age=3600")
	return c.Send(jpg)
}

func thumbKey(key string) string { return key + ".thumb" }

// thumbnail serves an image file's decrypted preview. Like downloads, it is
// decrypted per request with the session's key and never cached shared.
func (s *Server) thumbnail(c fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return err
	}
	a := mustAuth(c)
	o, err := s.Store.GetObject(c.Context(), a.UserID, id, store.KindFile)
	if err != nil {
		return err
	}
	dek, err := vault.DEK(a.MK, o)
	if err != nil {
		return err
	}
	defer crypto.Zero(dek)
	var meta fileMeta
	if err := vault.OpenWithDEK(dek, o, &meta); err != nil {
		return err
	}
	if !meta.Thumb {
		return errs.NotFound.New("no preview for this file")
	}
	sealed, err := s.Blobs.Get(c.Context(), thumbKey(o.S3Key.ValueOrZero()))
	if err != nil {
		return errs.Upstream.Wrap(err, "storage unavailable")
	}
	jpg, err := vault.OpenPart(dek, o.ID, "thumb", sealed)
	if err != nil {
		return err
	}
	// Our own re-encoded JPEG, so it is safe to render inline.
	c.Set("Content-Type", "image/jpeg")
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("Cache-Control", "private, max-age=3600")
	return c.Send(jpg)
}

func (s *Server) deleteFile(c fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return err
	}
	keys, err := s.Store.DeleteObject(c.Context(), mustAuth(c).UserID, id, store.KindFile)
	if err != nil {
		return err
	}
	for _, k := range keys {
		for _, k := range []string{k, thumbKey(k)} {
			if err := s.Blobs.Delete(c.Context(), k); err != nil {
				slog.Warn("delete blob", "key", k, "err", err)
			}
		}
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *Server) setFileOffline(c fiber.Ctx) error {
	var req offlineReq
	if err := c.Bind().Body(&req); err != nil {
		return err
	}
	id, err := paramID(c)
	if err != nil {
		return err
	}
	a := mustAuth(c)
	o, err := s.Store.GetObject(c.Context(), a.UserID, id, store.KindFile)
	if err != nil {
		return err
	}
	var meta fileMeta
	if err := vault.Open(a.MK, o, &meta); err != nil {
		return err
	}
	if err := s.setOffline(c, o, req.Enabled); err != nil {
		return err
	}
	view := viewFile(o, meta)
	view.Offline = req.Enabled
	return c.JSON(fiber.Map{"file": view})
}

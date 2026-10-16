package server

import (
	"github.com/gofiber/fiber/v3"

	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
	"github.com/reaganiwadha/adytus/apps/api/internal/store"
	"github.com/reaganiwadha/adytus/apps/api/internal/tools"
)

// toolState is a tool as the settings page shows it.
type toolState struct {
	tools.Info
	Enabled bool `json:"enabled"`
}

func (s *Server) settingsView(st store.Settings) fiber.Map {
	on := map[string]bool{}
	for _, id := range s.Tools.Enabled(st.Tools) {
		on[id] = true
	}
	list := []toolState{}
	for _, info := range s.Tools.Infos() {
		list = append(list, toolState{info, on[info.ID]})
	}
	return fiber.Map{"tools": list, "auto_title": st.AutoTitleOn()}
}

func (s *Server) getSettings(c fiber.Ctx) error {
	st, err := s.Store.Settings(c.Context(), mustAuth(c).UserID)
	if err != nil {
		return err
	}
	return c.JSON(s.settingsView(st))
}

// putSettings merges the given tool switches into the saved ones; fields
// left out keep their value.
func (s *Server) putSettings(c fiber.Ctx) error {
	var req struct {
		Tools     map[string]bool `json:"tools"`
		AutoTitle *bool           `json:"auto_title"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return err
	}
	a := mustAuth(c)
	st, err := s.Store.Settings(c.Context(), a.UserID)
	if err != nil {
		return err
	}
	if st.Tools == nil {
		st.Tools = map[string]bool{}
	}
	for id, on := range req.Tools {
		if !s.Tools.Has(id) {
			return errs.Validation.New("unknown tool %q", id)
		}
		st.Tools[id] = on
	}
	if req.AutoTitle != nil {
		st.AutoTitle = req.AutoTitle
	}
	if err := s.Store.SetSettings(c.Context(), a.UserID, st); err != nil {
		return err
	}
	return c.JSON(s.settingsView(st))
}

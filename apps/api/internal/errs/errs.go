// Package errs defines the API's error types and maps them to HTTP responses.
package errs

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v3"
	"github.com/joomcode/errorx"
)

var (
	ns = errorx.NewNamespace("adytus")

	NotFound       = ns.NewType("not_found", errorx.NotFound())
	Validation     = ns.NewType("validation")
	Unauthorized   = ns.NewType("unauthorized")
	Forbidden      = ns.NewType("forbidden")
	Conflict       = ns.NewType("conflict", errorx.Duplicate())
	BudgetExceeded = ns.NewType("budget_exceeded")
	Upstream       = ns.NewType("upstream")
	Internal       = ns.NewType("internal")
)

var statusByType = map[*errorx.Type]int{
	NotFound:       fiber.StatusNotFound,
	Validation:     fiber.StatusBadRequest,
	Unauthorized:   fiber.StatusUnauthorized,
	Forbidden:      fiber.StatusForbidden,
	Conflict:       fiber.StatusConflict,
	BudgetExceeded: fiber.StatusPaymentRequired,
	Upstream:       fiber.StatusBadGateway,
}

type body struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// Handler is the Fiber error handler. Internal errors are logged and hidden
// from the client.
func Handler(c fiber.Ctx, err error) error {
	var verrs validator.ValidationErrors
	if errors.As(err, &verrs) {
		return c.Status(fiber.StatusBadRequest).JSON(body{"validation", verrs.Error()})
	}

	var ferr *fiber.Error
	if errors.As(err, &ferr) {
		return c.Status(ferr.Code).JSON(body{"http", ferr.Message})
	}

	if xerr := errorx.Cast(err); xerr != nil {
		if status, ok := statusByType[xerr.Type()]; ok {
			return c.Status(status).JSON(body{strings.TrimPrefix(xerr.Type().FullName(), "adytus."), xerr.Message()})
		}
	}

	slog.Error("unhandled error", "path", c.Path(), "err", err)
	return c.Status(fiber.StatusInternalServerError).JSON(body{"internal", "internal error"})
}

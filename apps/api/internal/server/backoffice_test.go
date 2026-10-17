package server

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func (e *env) makeAdmin(email string) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), `UPDATE users SET is_admin = true WHERE email = $1`, email); err != nil {
		e.t.Fatal(err)
	}
}

func TestBackofficeHiddenFromUsers(t *testing.T) {
	e := newEnv(t)
	e.signup("user@example.com")

	me := e.must(200, "GET", "/api/me", nil)
	if privs := me["privileges"].([]any); len(privs) != 0 {
		t.Fatalf("privileges = %v, want none", privs)
	}
	for _, path := range []string{"/api/backoffice", "/api/backoffice/ui/backoffice.js"} {
		e.must(404, "GET", path, nil)
	}
	e.must(404, "PUT", "/api/backoffice/default", map[string]string{"model": "pricey"})

	e.cookie = ""
	e.must(401, "GET", "/api/backoffice/ui/backoffice.js", nil)
}

func TestBackofficeUIForAdmins(t *testing.T) {
	e := newEnv(t)
	e.signup("admin@example.com")
	e.makeAdmin("admin@example.com")

	me := e.must(200, "GET", "/api/me", nil)
	if privs := me["privileges"].([]any); len(privs) != 1 || privs[0] != "backoffice" {
		t.Fatalf("privileges = %v", privs)
	}
	resp, _ := e.send(httptest.NewRequest("GET", "/api/backoffice/ui/backoffice.js", nil))
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "mount") {
		t.Fatalf("ui: %d %s", resp.StatusCode, body)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("Cache-Control = %q", cc)
	}

	// Revoking takes effect on the next request.
	if _, err := e.pool.Exec(context.Background(), `UPDATE users SET is_admin = false`); err != nil {
		t.Fatal(err)
	}
	e.must(404, "GET", "/api/backoffice/ui/backoffice.js", nil)
}

func TestBackofficeProvidersAndModels(t *testing.T) {
	e := newEnv(t)
	e.signup("admin@example.com")
	e.makeAdmin("admin@example.com")

	spec := map[string]any{
		"provider": "openrouter", "upstream": "vendor/model", "name": "Routed",
		"prompt_usd_per_mtok": 1, "completion_usd_per_mtok": 2,
		"context_tokens": 8000, "max_output_tokens": 500, "enabled": true,
	}
	// OpenRouter has no key yet, so a model on it is refused.
	e.must(400, "PUT", "/api/backoffice/models/routed", spec)

	const key = "sk-or-v1-secret-abcd"
	st := e.must(200, "PUT", "/api/backoffice/providers/openrouter",
		map[string]any{"base_url": "https://openrouter.ai/api/v1", "api_key": key})
	resp, _ := e.do("GET", "/api/backoffice", nil)
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), "secret") {
		t.Fatalf("backoffice leaks the key: %s", raw)
	}
	for _, p := range st["providers"].([]any) {
		p := p.(map[string]any)
		if p["id"] == "openrouter" && (p["key_set"] != true || p["key_hint"] != "abcd") {
			t.Fatalf("openrouter = %v", p)
		}
	}
	var sealed []byte
	if err := e.pool.QueryRow(context.Background(), `SELECT sealed_key FROM llm_providers WHERE id = 'openrouter'`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "secret") {
		t.Fatal("key stored in the clear")
	}

	e.must(200, "PUT", "/api/backoffice/models/routed", spec)
	e.must(200, "PUT", "/api/backoffice/default", map[string]string{"model": "routed"})
	models := e.must(200, "GET", "/api/models", nil)
	if models["default"] != "routed" {
		t.Fatalf("default = %v", models["default"])
	}

	// Clearing the key while a model needs it is refused and changes nothing.
	e.must(400, "PUT", "/api/backoffice/providers/openrouter",
		map[string]any{"base_url": "https://openrouter.ai/api/v1", "api_key": ""})

	// A disabled model leaves the user catalog; the default falls back.
	spec["enabled"] = false
	e.must(200, "PUT", "/api/backoffice/models/routed", spec)
	models = e.must(200, "GET", "/api/models", nil)
	for _, m := range models["models"].([]any) {
		if m.(map[string]any)["id"] == "routed" {
			t.Fatal("disabled model still offered")
		}
	}
	if models["default"] != "cheap" {
		t.Fatalf("default = %v, want the first model", models["default"])
	}
	e.must(400, "PUT", "/api/backoffice/default", map[string]string{"model": "routed"})

	for _, id := range []string{"pricey", "hands", "eyes", "routed"} {
		e.must(200, "DELETE", "/api/backoffice/models/"+id, nil)
	}
	e.must(400, "DELETE", "/api/backoffice/models/cheap", nil)
	e.must(404, "PUT", "/api/backoffice/providers/nope", map[string]any{"base_url": "http://x"})
}

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
	for _, m := range e.must(200, "GET", "/api/models", nil)["models"].([]any) {
		m := m.(map[string]any)
		for _, key := range []string{"prompt_usd_per_mtok", "completion_usd_per_mtok", "identity", "upstream", "provider"} {
			if _, ok := m[key]; ok {
				t.Fatalf("non-admin saw the model's %s: %v", key, m)
			}
		}
	}
	for _, path := range []string{"/api/backoffice", "/api/backoffice/users", "/api/backoffice/ui/backoffice.js"} {
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
	for _, m := range models["models"].([]any) {
		m := m.(map[string]any)
		if m["id"] == "routed" {
			if m["prompt_usd_per_mtok"] != float64(1) || m["completion_usd_per_mtok"] != float64(2) {
				t.Fatalf("admin lost model prices: %v", m)
			}
		}
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

func TestBackofficeSingleModelAndRebrand(t *testing.T) {
	e := newEnv(t)
	e.signup("admin@example.com")
	e.makeAdmin("admin@example.com")
	e.fund()

	// Rebrand cheap: users see our name and blurb, and the model is told to
	// introduce itself that way.
	spec := map[string]any{
		"provider": "ollama", "upstream": "up-cheap", "name": "Adytus One",
		"description": "Our own assistant.", "identity": "You are Adytus One.",
		"prompt_usd_per_mtok": 1, "completion_usd_per_mtok": 2,
		"context_tokens": 8000, "max_output_tokens": 500, "enabled": true,
	}
	e.must(200, "PUT", "/api/backoffice/models/cheap", spec)
	for _, m := range e.must(200, "GET", "/api/models", nil)["models"].([]any) {
		m := m.(map[string]any)
		if m["id"] == "cheap" && (m["name"] != "Adytus One" || m["description"] != "Our own assistant.") {
			t.Fatalf("rebrand not offered: %v", m)
		}
	}

	// A chat on another model is moved onto the one model when it's sent to.
	hands := e.must(201, "POST", "/api/chats", map[string]string{"model": "hands"})["chat"].(map[string]any)["id"].(string)
	e.must(200, "PUT", "/api/backoffice/single", map[string]string{"model": "cheap"})
	models := e.must(200, "GET", "/api/models", nil)
	if len(models["models"].([]any)) != 1 || models["single"] != "cheap" || models["default"] != "cheap" {
		t.Fatalf("single model mode offers %v", models)
	}
	e.must(400, "POST", "/api/chats", map[string]string{"model": "hands"})

	status, events := e.sendMessage(hands, "hi")
	if status != 200 || last(events).Name != "done" {
		t.Fatalf("send on a moved chat: %d %+v", status, events)
	}
	if events[0].Name != "start" || events[0].Data["model"] != "cheap" {
		t.Fatalf("start event: %+v", events[0])
	}
	if chat := e.must(200, "GET", "/api/chats/"+hands, nil)["chat"].(map[string]any); chat["model"] != "cheap" {
		t.Fatalf("chat model = %v, want cheap", chat["model"])
	}
	if got := e.llm.lastMsgs[0].Content; got != "sys\n\nYou are Adytus One." {
		t.Fatalf("system prompt = %q", got)
	}

	// Off offers the enabled models again; an unknown model is refused.
	e.must(200, "PUT", "/api/backoffice/single", map[string]string{"model": ""})
	models = e.must(200, "GET", "/api/models", nil)
	if models["single"] != "" || len(models["models"].([]any)) < 2 {
		t.Fatalf("single model mode stayed on: %v", models)
	}
	e.must(404, "PUT", "/api/backoffice/single", map[string]string{"model": "nope"})
}

func TestBackofficeUserUsage(t *testing.T) {
	e := newEnv(t)
	e.signup("admin@example.com")
	e.makeAdmin("admin@example.com")
	e.fund()

	chatID := e.must(201, "POST", "/api/chats", map[string]string{"model": "cheap"})["chat"].(map[string]any)["id"].(string)
	if status, ev := e.sendMessage(chatID, "hi"); status != 200 || last(ev).Name != "done" {
		t.Fatalf("send: %d %+v", status, ev)
	}

	users := e.must(200, "GET", "/api/backoffice/users", nil)["users"].([]any)
	if len(users) != 1 {
		t.Fatalf("users = %v", users)
	}
	u := users[0].(map[string]any)
	if u["email"] != "admin@example.com" || u["is_admin"] != true {
		t.Fatalf("user = %v", u)
	}
	if num(u["purchased_tokens"]) != 5000 || num(u["available_tokens"]) != 4998 || num(u["used_tokens"]) != 2 {
		t.Fatalf("token balance = %v", u)
	}
	if num(u["prompt_tokens"]) != 1000 || num(u["completion_tokens"]) != 100 || num(u["requests"]) != 1 {
		t.Fatalf("usage = %v", u)
	}
}

func TestNearPaymentsFromBackoffice(t *testing.T) {
	e := newEnv(t)
	e.signup("admin@example.com")

	// Off by default: no NEAR for users, and no vouchers with nothing to pay with.
	out := e.must(200, "GET", "/api/billing/near", nil)
	if out["enabled"] != false {
		t.Fatalf("near enabled = %v", out["enabled"])
	}
	e.must(404, "POST", "/api/billing/near/quote", map[string]any{"usd_micros": 10_000_000})
	e.must(404, "POST", "/api/billing/voucher", map[string]any{"code": "NOPE"})

	// Only admins see the settings.
	e.must(404, "GET", "/api/backoffice/near", nil)
	e.makeAdmin("admin@example.com")
	state := e.must(200, "GET", "/api/backoffice/near", nil)
	s := state["settings"].(map[string]any)
	if s["enabled"] != false || s["network"] != "mainnet" || s["contract"] != "" {
		t.Fatalf("default settings %v", s)
	}

	// Saved while off, nothing is asked of the chain.
	s["contract"] = "Pay.Adytus.Near"
	s["poll_secs"] = 9
	state = e.must(200, "PUT", "/api/backoffice/near/settings", s)
	s = state["settings"].(map[string]any)
	if s["contract"] != "pay.adytus.near" || s["poll_secs"] != float64(9) {
		t.Fatalf("saved settings %v", s)
	}

	// Bad values are refused.
	bad := map[string]any{}
	for k, v := range s {
		bad[k] = v
	}
	bad["tolerance_bps"] = 5_000
	e.must(400, "PUT", "/api/backoffice/near/settings", bad)

	// Switching on checks the RPC first; this one doesn't answer.
	s["enabled"] = true
	s["rpc_url"] = "http://127.0.0.1:1"
	s["price_url"] = "http://127.0.0.1:1"
	e.must(400, "PUT", "/api/backoffice/near/settings", s)
	if out := e.must(200, "GET", "/api/billing/near", nil); out["enabled"] != false {
		t.Fatal("NEAR switched on against a dead RPC")
	}

	// The test reports each check rather than failing.
	checks := e.must(200, "POST", "/api/backoffice/near/test", s)["checks"].([]any)
	if len(checks) == 0 || checks[0].(map[string]any)["ok"] != false {
		t.Fatalf("checks %v", checks)
	}
}

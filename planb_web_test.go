package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBWebRequiresTokenAndSameOrigin(t *testing.T) {
	app := &bWeb{config: bWebConfig{ServerID: "0123456789ab", Token: strings.Repeat("a", 64)}, cookie: strings.Repeat("b", 64), closing: context.Background()}
	send := func(method, path, body, origin string, cookie bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:12345"+path, strings.NewReader(body))
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if cookie {
			r.AddCookie(&http.Cookie{Name: app.cookieName(), Value: app.cookie})
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	if got := send("GET", "/api/state", "", "", false); got.Code != 401 {
		t.Fatal(got.Code)
	}
	if got := send("POST", "/api/login", `{"token":"bad"}`, "http://127.0.0.1:12345", false); got.Code != 401 {
		t.Fatal(got.Code)
	}
	body := `{"token":"` + app.config.Token + `"}`
	if got := send("POST", "/api/login", body, "http://attacker.example", false); got.Code != 403 {
		t.Fatal(got.Code)
	}
	got := send("POST", "/api/login", body, "http://127.0.0.1:12345", false)
	if got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	cookies := got.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("weak browser cookie")
	}
	if got := send("POST", "/api/sessions", `{"device_id":"../../other","project":"/tmp","kind":"shell"}`, "http://127.0.0.1:12345", true); got.Code != 400 {
		t.Fatal(got.Code)
	}
	if got := send("GET", "/api/terminal?session=other-tmux", "", "", true); got.Code != 400 {
		t.Fatal(got.Code)
	}
	if got := send("GET", "/api/files?device=../../other&path=/", "", "", true); got.Code != 400 {
		t.Fatal(got.Code)
	}
	if got := send("POST", "/api/logout", "{}", "http://127.0.0.1:12345", true); got.Code != 200 || got.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout failed")
	}
}
func TestBWebStaticHasNoExternalRuntimeOrInlineScript(t *testing.T) {
	app := &bWeb{}
	r := httptest.NewRequest("GET", "http://127.0.0.1/", nil)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "<script>") || strings.Contains(body, "http://") || strings.Contains(body, "https://") {
		t.Fatal("external or inline script")
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("missing frame protection")
	}
	if !strings.Contains(body, `for="project"`) {
		t.Fatal("unlabelled project input")
	}
}
func TestBNativePromptSeparatesRemoteArtifactsAndLocalExecution(t *testing.T) {
	r := bSessionRequest{DeviceID: "0123456789ab", Project: "/tmp/project"}
	prompt := bLocalPrompt(r, "/data/devices/0123456789ab/ssh_config")
	for _, required := range []string{"project directory is not a sandbox", "Never substitute execution on the remote Harness", "do not blindly replay", "CLAUDE.md/AGENTS.md"} {
		if !strings.Contains(prompt, required) {
			t.Fatal("missing execution contract", required)
		}
	}
}
func TestBWebJSONRejectsMultipleValuesAndUnknownKeys(t *testing.T) {
	for _, body := range []string{`{"token":"a"} {"token":"b"}`, `{"token":"a","extra":true}`} {
		var out struct {
			Token string `json:"token"`
		}
		r := httptest.NewRequest("POST", "http://localhost", bytes.NewBufferString(body))
		if bDecode(httptest.NewRecorder(), r, &out) == nil {
			t.Fatal("accepted", body)
		}
	}
}

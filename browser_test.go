package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestBrowserViewerHasAuthOriginAndActionBoundaries(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer c.CloseNow()
		for {
			_, data, e := c.Read(r.Context())
			if e != nil {
				return
			}
			var request struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(data, &request) != nil {
				return
			}
			result := map[string]any{}
			if request.Method == "Page.captureScreenshot" {
				result["data"] = base64.StdEncoding.EncodeToString([]byte("jpeg-fixture"))
			}
			b, _ := json.Marshal(map[string]any{"id": request.ID, "result": result})
			if c.Write(r.Context(), websocket.MessageText, b) != nil {
				return
			}
		}
	}))
	defer backend.Close()
	conn, _, e := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(backend.URL, "http"), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.CloseNow()
	viewer := &browserViewer{client: &cdpClient{conn: conn}, token: "test-only-capability"}
	for _, test := range []struct {
		path, method, host, origin, token, body string
		status                                  int
	}{
		{"/api/frame", "GET", "127.0.0.1:1234", "", "", "", 401},
		{"/api/frame", "GET", "evil.example:1234", "", "test-only-capability", "", 421},
		{"/api/frame", "GET", "127.0.0.1:1234", "https://evil.example", "test-only-capability", "", 403},
		{"/api/frame", "GET", "127.0.0.1:1234", "", "test-only-capability", "", 200},
		{"/api/action", "POST", "127.0.0.1:1234", "", "test-only-capability", `{"action":"navigate","url":"file:///etc/passwd"}`, 400},
		{"/api/action", "POST", "127.0.0.1:1234", "", "test-only-capability", `{"action":"rawCDP"}`, 400},
		{"/api/action", "POST", "127.0.0.1:1234", "", "test-only-capability", `{"action":"navigate","url":"https://example.org"}`, 200},
	} {
		r := httptest.NewRequest(test.method, "http://"+test.host+test.path, strings.NewReader(test.body))
		r.Host = test.host
		if test.origin != "" {
			r.Header.Set("Origin", test.origin)
		}
		if test.token != "" {
			r.Header.Set("Authorization", "Bearer "+test.token)
		}
		w := httptest.NewRecorder()
		viewer.ServeHTTP(w, r)
		if w.Code != test.status {
			b, _ := io.ReadAll(w.Result().Body)
			t.Fatalf("%s %s: got %d want %d: %s", test.method, test.path, w.Code, test.status, b)
		}
	}
}

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRealBrowserViewer(t *testing.T) {
	binary := os.Getenv("RDEV_TEST_BINARY")
	if binary == "" {
		t.Skip("set RDEV_TEST_BINARY to opt into launching an isolated headless Chrome")
	}
	if os.Getuid() == 0 || chromeExecutable() == "" {
		t.Skip("requires a normal user and Chrome")
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<!doctype html><title>RDEV browser acceptance</title><style>input{position:absolute;left:20px;top:20px;width:300px;height:40px;font-size:24px}</style><input id="entry" aria-label="entry"><h1 style="margin-top:100px">Remote development browser</h1>`)
	}))
	defer fixture.Close()
	root := filepath.Join(t.TempDir(), "Custom Browser 资料")
	cmd := exec.Command(binary, "remote", "browser", root)
	keepalive, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	defer keepalive.Close()
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	var errors bytes.Buffer
	cmd.Stderr = &errors
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(12 * time.Second):
			cmd.Process.Kill()
			<-done
			t.Error("viewer did not terminate gracefully")
		}
	}()
	var info browserInfo
	scan := bufio.NewScanner(stdout)
	var value strings.Builder
	ready := make(chan bool, 1)
	go func() {
		for scan.Scan() {
			value.WriteString(scan.Text())
			value.WriteByte('\n')
			if json.Unmarshal([]byte(value.String()), &info) == nil && info.Port > 0 {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("browser startup failed:", errors.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("browser startup timeout")
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", info.Port)
	request := func(method, path, body string, auth bool) *http.Response {
		t.Helper()
		r, _ := http.NewRequest(method, endpoint+path, strings.NewReader(body))
		if auth {
			r.Header.Set("Authorization", "Bearer "+info.Token)
		}
		r.Header.Set("Content-Type", "application/json")
		c := http.Client{Timeout: 15 * time.Second}
		response, e := c.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		return response
	}
	r := request("GET", "/api/frame", "", false)
	r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatal("unauthorized frame was readable")
	}
	action := func(data map[string]any) {
		t.Helper()
		b, _ := json.Marshal(data)
		r := request("POST", "/api/action", string(b), true)
		defer r.Body.Close()
		if r.StatusCode != 200 {
			b, _ := io.ReadAll(r.Body)
			t.Fatalf("browser action failed: %d %s", r.StatusCode, b)
		}
	}
	action(map[string]any{"action": "navigate", "url": fixture.URL})
	var imageData []byte
	for i := 0; i < 20; i++ {
		r = request("GET", "/api/frame", "", true)
		imageData, e = io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode == 200 && e == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	image, e := jpeg.Decode(bytes.NewReader(imageData))
	if e != nil || image.Bounds().Dx() < 800 {
		t.Fatal("no usable browser screenshot", e)
	}
	action(map[string]any{"action": "click", "x": 100, "y": 40})
	action(map[string]any{"action": "text", "text": "hello remote dev"})
	r, e = http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", info.CDPPort))
	if e != nil {
		t.Fatal(e)
	}
	var targets []struct {
		Type string `json:"type"`
		WS   string `json:"webSocketDebuggerUrl"`
	}
	e = json.NewDecoder(r.Body).Decode(&targets)
	r.Body.Close()
	if e != nil {
		t.Fatal(e)
	}
	var ws string
	for _, target := range targets {
		if target.Type == "page" {
			ws = target.WS
			break
		}
	}
	conn, _, e := websocket.Dial(context.Background(), ws, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.CloseNow()
	client := &cdpClient{conn: conn}
	b, e := client.call("Runtime.evaluate", map[string]any{"expression": "document.getElementById('entry').value", "returnByValue": true})
	if e != nil {
		t.Fatal(e)
	}
	var result struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if json.Unmarshal(b, &result) != nil || result.Result.Value != "hello remote dev" {
		t.Fatalf("real browser input mismatch: %s", b)
	}
	if evidence := os.Getenv("RDEV_TEST_EVIDENCE"); evidence != "" {
		if e = os.MkdirAll(evidence, 0700); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(evidence, "browser.jpg"), imageData, 0600); e != nil {
			t.Fatal(e)
		}
	}
	t.Logf("real headless browser navigation, %dx%d screenshot, click, text and authentication passed", image.Bounds().Dx(), image.Bounds().Dy())
}

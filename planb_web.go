package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed web/plan-b
var bWebFiles embed.FS

type bWebConfig struct {
	ServerID string `json:"server_id"`
	Token    string `json:"token"`
}
type bWeb struct {
	config   bWebConfig
	cookie   string
	mu       sync.Mutex
	closing  context.Context
	ttySlots chan struct{}
	deviceMu sync.Mutex
	devices  map[string]*bMux
}
type bSessionInfo struct {
	ID      string    `json:"id"`
	Device  string    `json:"device"`
	Project string    `json:"project"`
	Kind    string    `json:"kind"`
	Created time.Time `json:"created"`
	Active  bool      `json:"active"`
}

var bSessionPattern = regexp.MustCompile(`^b-[a-f0-9]{12}-[a-f0-9]{12}$`)

func bWriteWebConfig() error {
	var c bWebConfig
	if e := json.NewDecoder(io.LimitReader(os.Stdin, 4096)).Decode(&c); e != nil {
		return e
	}
	if !bIDPattern.MatchString(c.ServerID) || len(c.Token) != 64 {
		return errors.New("非法 B Web 配置")
	}
	if e := os.MkdirAll("/data/config", 0700); e != nil {
		return e
	}
	b, _ := json.Marshal(c)
	return atomicWrite("/data/config/web.json", b, 0600)
}
func bWebServe() error {
	var c bWebConfig
	b, e := os.ReadFile("/data/config/web.json")
	if e != nil {
		return e
	}
	if e = json.Unmarshal(b, &c); e != nil || len(c.Token) != 64 {
		return errors.New("Web 配置缺失")
	}
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	m := hmac.New(sha256.New, []byte(c.Token))
	m.Write([]byte("rdev-b-web-session-v1"))
	app := &bWeb{config: c, cookie: hex.EncodeToString(m.Sum(nil)), closing: ctx, ttySlots: make(chan struct{}, 16)}
	server := &http.Server{Addr: "0.0.0.0:8788", Handler: app, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16 * 1024, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	go app.scheduler(ctx)
	e = server.ListenAndServe()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func (w *bWeb) cookieName() string { return "rdev_b_" + w.config.ServerID }
func (w *bWeb) authenticated(r *http.Request) bool {
	c, e := r.Cookie(w.cookieName())
	return e == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(w.cookie)) == 1
}
func bJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func bWebError(w http.ResponseWriter, e error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(400)
	bJSON(w, map[string]string{"error": e.Error()})
}
func bDecode(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 32768)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(out); e != nil {
		return e
	}
	var extra any
	if e := decoder.Decode(&extra); e != io.EOF {
		return errors.New("无效 JSON 请求")
	}
	return nil
}
func (w *bWeb) ServeHTTP(out http.ResponseWriter, r *http.Request) {
	out.Header().Set("Cache-Control", "no-store")
	out.Header().Set("X-Content-Type-Options", "nosniff")
	out.Header().Set("Referrer-Policy", "no-referrer")
	out.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; object-src 'none'")
	if r.Method != "GET" && r.Method != "HEAD" {
		if r.Header.Get("Origin") != "" {
			origin, e := url.Parse(r.Header.Get("Origin"))
			if e != nil || origin.Host != r.Host || origin.Scheme != "http" {
				http.Error(out, "origin refused", 403)
				return
			}
		}
	}
	if r.URL.Path == "/health" {
		bJSON(out, map[string]any{"protocol": bProtocol})
		return
	}
	if r.URL.Path == "/api/login" && r.Method == "POST" {
		var body struct {
			Token string `json:"token"`
		}
		if bDecode(out, r, &body) != nil || subtle.ConstantTimeCompare([]byte(body.Token), []byte(w.config.Token)) != 1 {
			http.Error(out, "unauthorized", 401)
			return
		}
		http.SetCookie(out, &http.Cookie{Name: w.cookieName(), Value: w.cookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 86400})
		bJSON(out, map[string]bool{"authenticated": true})
		return
	}
	if r.URL.Path == "/api/device" {
		w.device(out, r)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		sub, _ := fs.Sub(bWebFiles, "web/plan-b")
		http.FileServer(http.FS(sub)).ServeHTTP(out, r)
		return
	}
	if !w.authenticated(r) {
		http.Error(out, "请从 rdev plan-b web 打开入口", 401)
		return
	}
	switch r.URL.Path {
	case "/api/logout":
		if r.Method != "POST" {
			http.Error(out, "method", 405)
			return
		}
		http.SetCookie(out, &http.Cookie{Name: w.cookieName(), Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		bJSON(out, map[string]bool{"authenticated": false})
	case "/api/state":
		if r.Method != "GET" {
			http.Error(out, "method", 405)
			return
		}
		sessions, e := bListSessions()
		if e != nil {
			bWebError(out, e)
			return
		}
		devices := bListDevices()
		bJSON(out, map[string]any{"sessions": sessions, "devices": devices, "schedules": w.readSchedules(), "protocol": bProtocol})
	case "/api/sessions":
		if r.Method != "POST" {
			http.Error(out, "method", 405)
			return
		}
		var req bSessionRequest
		if e := bDecode(out, r, &req); e != nil {
			bWebError(out, e)
			return
		}
		if req.ID == "" {
			req.ID = bRandom(6)
		}
		if req.Kind != "claude" && req.Kind != "shell" && req.Kind != "account" {
			bWebError(out, errors.New("会话类型无效"))
			return
		}
		info, e := bCreateSession(req)
		if e != nil {
			bWebError(out, e)
			return
		}
		bJSON(out, info)
	case "/api/terminal":
		w.terminal(out, r)
	case "/api/files":
		w.files(out, r)
	case "/api/schedules":
		w.schedules(out, r)
	default:
		http.NotFound(out, r)
	}
}
func bListDevices() []bDevice {
	out := []bDevice{}
	entries, _ := os.ReadDir("/data/devices")
	for _, entry := range entries {
		if !entry.IsDir() || !bIDPattern.MatchString(entry.Name()) {
			continue
		}
		var d bDevice
		b, e := os.ReadFile("/data/devices/" + entry.Name() + "/device.json")
		if e == nil && json.Unmarshal(b, &d) == nil {
			d.ClientKey = ""
			out = append(out, d)
		}
	}
	return out
}
func bListSessions() ([]bSessionInfo, error) {
	out := []bSessionInfo{}
	entries, e := os.ReadDir("/data/sessions")
	if os.IsNotExist(e) {
		return out, nil
	}
	if e != nil {
		return nil, e
	}
	active, _ := capture("tmux", []string{"list-sessions", "-F", "#{session_name}"}, nil)
	set := map[string]bool{}
	for _, name := range strings.Fields(string(active)) {
		set[name] = true
	}
	for _, entry := range entries {
		if len(out) >= 1000 {
			break
		}
		var s bSessionInfo
		b, e := os.ReadFile("/data/sessions/" + entry.Name())
		if e == nil && json.Unmarshal(b, &s) == nil && bSessionPattern.MatchString(s.ID) {
			s.Active = set[s.ID]
			out = append(out, s)
		}
	}
	return out, nil
}
func bCreateSession(r bSessionRequest) (bSessionInfo, error) {
	var info bSessionInfo
	if !bIDPattern.MatchString(r.ID) || !bIDPattern.MatchString(r.DeviceID) || strings.ContainsAny(r.Project, "\r\n\x00") || r.Project == "" {
		return info, errors.New("请选择设备并填写本机项目路径")
	}
	conf := "/data/devices/" + r.DeviceID + "/ssh_config"
	if r.Kind != "account" {
		if _, e := capture("ssh", []string{"-F", conf, "device", "exit 0"}, nil); e != nil {
			return info, errors.New("本机执行器离线；在对应电脑运行 rdev plan-b on，不会切换到服务器执行")
		}
	}
	sessions, _ := capture("tmux", []string{"list-sessions", "-F", "#{session_name}"}, nil)
	if len(strings.Fields(string(sessions))) >= 32 {
		return info, errors.New("活动终端已达 32 个；请先结束不需要的会话")
	}
	hash := sha256.Sum256([]byte(r.Project))
	work := "/data/workspace/" + r.DeviceID + "/" + hex.EncodeToString(hash[:6])
	if e := os.MkdirAll(work, 0700); e != nil {
		return info, e
	}
	program := append([]string{"claude", "--append-system-prompt", bLocalPrompt(r, conf)}, r.Args...)
	if r.Kind == "shell" {
		program = []string{"bash"}
	}
	if r.Kind == "account" {
		program = []string{"claude", "auth", "login"}
	}
	name := "b-" + r.DeviceID + "-" + r.ID
	if _, e := capture("tmux", []string{"new-session", "-d", "-s", name, "-c", work, "exec " + shellJoin(program)}, nil); e != nil {
		return info, e
	}
	info = bSessionInfo{ID: name, Device: r.DeviceID, Project: r.Project, Kind: r.Kind, Created: time.Now().UTC(), Active: true}
	if e := os.MkdirAll("/data/sessions", 0700); e != nil {
		return info, e
	}
	b, _ := json.Marshal(info)
	if e := atomicWrite("/data/sessions/"+name+".json", b, 0600); e != nil {
		return info, e
	}
	return info, nil
}
func (w *bWeb) terminal(out http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(out, "method", 405)
		return
	}
	name := r.URL.Query().Get("session")
	if !bSessionPattern.MatchString(name) {
		http.Error(out, "invalid session", 400)
		return
	}
	if _, e := os.Stat("/data/sessions/" + name + ".json"); e != nil {
		http.Error(out, "session missing", 404)
		return
	}
	select {
	case w.ttySlots <- struct{}{}:
		defer func() { <-w.ttySlots }()
	default:
		http.Error(out, "too many terminal viewers", 429)
		return
	}
	conn, e := websocket.Accept(out, r, nil)
	if e != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(65536)
	ctx, cancel := context.WithCancel(w.closing)
	defer cancel()
	pty, e := bOpenPTY([]string{"tmux", "attach-session", "-t", "=" + name}, 30, 100)
	if e != nil {
		conn.Close(websocket.StatusInternalError, "terminal unavailable")
		return
	}
	defer pty.Close()
	var pending atomic.Int64
	acked := make(chan struct{}, 1)
	go func() {
		defer cancel()
		buf := make([]byte, 16384)
		for {
			n, e := pty.File.Read(buf)
			if n > 0 {
				for pending.Load() >= 262144 {
					select {
					case <-ctx.Done():
						return
					case <-acked:
					}
				}
				pending.Add(int64(n))
				if e = conn.Write(ctx, websocket.MessageBinary, buf[:n]); e != nil {
					return
				}
			}
			if e != nil {
				return
			}
		}
	}()
	for {
		kind, b, e := conn.Read(ctx)
		if e != nil {
			return
		}
		if kind == websocket.MessageBinary {
			if _, e = pty.File.Write(b); e != nil {
				return
			}
			continue
		}
		var size struct {
			Rows uint16 `json:"rows"`
			Cols uint16 `json:"cols"`
			Ack  int64  `json:"ack"`
		}
		if json.Unmarshal(b, &size) == nil && size.Ack > 0 {
			if size.Ack > pending.Load() {
				return
			}
			pending.Add(-size.Ack)
			select {
			case acked <- struct{}{}:
			default:
			}
			continue
		}
		if json.Unmarshal(b, &size) == nil && size.Rows >= 2 && size.Rows <= 200 && size.Cols >= 20 && size.Cols <= 400 {
			_ = pty.Resize(size.Rows, size.Cols)
		}
	}
}

package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type browserInfo struct {
	Port    int    `json:"port"`
	CDPPort int    `json:"cdp_port"`
	Token   string `json:"token"`
}
type cdpClient struct {
	conn *websocket.Conn
	mu   sync.Mutex
	id   int
}

func (c *cdpClient) call(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.id++
	id := c.id
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if e := c.conn.Write(ctx, websocket.MessageText, b); e != nil {
		return nil, e
	}
	for {
		_, b, e := c.conn.Read(ctx)
		if e != nil {
			return nil, e
		}
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if e = json.Unmarshal(b, &response); e != nil {
			return nil, e
		}
		if response.ID != id {
			continue
		}
		if len(response.Error) > 0 {
			return nil, errors.New("浏览器拒绝该操作")
		}
		return response.Result, nil
	}
}
func chromeExecutable() string {
	home, _ := os.UserHomeDir()
	if b, e := os.ReadFile(filepath.Join(home, ".rdev", "runtime", "chrome.json")); e == nil {
		var v map[string]string
		if json.Unmarshal(b, &v) == nil {
			if info, e := os.Stat(v["path"]); e == nil && !info.IsDir() {
				return v["path"]
			}
		}
	}
	for _, candidate := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"} {
		if p, e := exec.LookPath(candidate); e == nil {
			return p
		}
	}
	return ""
}
func serveBrowser(root string) error {
	if os.Getuid() == 0 {
		return errors.New("浏览器必须使用普通用户运行；不会禁用 Chromium 沙箱")
	}
	exe := chromeExecutable()
	if exe == "" {
		return errors.New("未找到 Chrome/Chromium；运行 tools install browser，或安装远端浏览器")
	}
	dir := filepath.Join(root, ".browser")
	profile := filepath.Join(dir, "profile")
	if e := os.MkdirAll(profile, 0700); e != nil {
		return e
	}
	var chrome *exec.Cmd
	active := filepath.Join(profile, "DevToolsActivePort")
	port := 0
	readPort := func() int {
		b, _ := os.ReadFile(active)
		line := strings.Split(string(b), "\n")[0]
		n, _ := strconv.Atoi(line)
		return n
	}
	if p := readPort(); p > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/json/version", p), nil)
		r, e := http.DefaultClient.Do(req)
		cancel()
		if e == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				port = p
			}
		}
	}
	if port == 0 {
		_ = os.Remove(active)
		log, e := os.OpenFile(filepath.Join(dir, "chrome.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		defer log.Close()
		chrome = exec.Command(exe, "--headless=new", "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0", "--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check", "--window-size=1280,800", "about:blank")
		privateProcess(chrome)
		chrome.Stdout = log
		chrome.Stderr = log
		if e = chrome.Start(); e != nil {
			return e
		}
		defer func() {
			if chrome.Process != nil {
				stopProcess(chrome)
				chrome.Wait()
			}
		}()
		for i := 0; i < 100; i++ {
			port = readPort()
			if port > 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if port == 0 {
			return errors.New("浏览器未就绪，查看远端研发目录 .browser/chrome.log")
		}
	}
	address := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	response, e := http.Get(address)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	var targets []struct {
		Type string `json:"type"`
		WS   string `json:"webSocketDebuggerUrl"`
	}
	if e = json.NewDecoder(response.Body).Decode(&targets); e != nil {
		return e
	}
	ws := ""
	for _, target := range targets {
		if target.Type == "page" {
			ws = target.WS
			break
		}
	}
	if ws == "" {
		return errors.New("浏览器没有可用页面")
	}
	conn, _, e := websocket.Dial(context.Background(), ws, nil)
	if e != nil {
		return e
	}
	conn.SetReadLimit(16 * 1024 * 1024)
	defer conn.CloseNow()
	client := &cdpClient{conn: conn}
	if _, e = client.call("Page.enable", map[string]any{}); e != nil {
		return e
	}
	secret := make([]byte, 32)
	if _, e = rand.Read(secret); e != nil {
		return e
	}
	token := hex.EncodeToString(secret)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return e
	}
	defer listener.Close()
	viewer := &browserViewer{client: client, token: token}
	state := browserInfo{Port: listener.Addr().(*net.TCPAddr).Port, CDPPort: port, Token: token}
	b, _ := json.Marshal(map[string]any{"cdp_port": port})
	if e = atomicWrite(filepath.Join(dir, "state.json"), b, 0600); e != nil {
		return e
	}
	if e = output(state); e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	go func() { io.Copy(io.Discard, os.Stdin); stop() }()
	server := &http.Server{Handler: viewer, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16 * 1024}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type browserViewer struct {
	client *cdpClient
	token  string
}

func (v *browserViewer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, _, e := net.SplitHostPort(r.Host)
	if e != nil || host != "127.0.0.1" && host != "localhost" {
		http.Error(w, "Invalid host", 421)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		if e != nil || u.Scheme != "http" || u.Host != r.Host {
			http.Error(w, "Invalid origin", 403)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.URL.Path == "/" && r.Method == "GET" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, browserHTML)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+v.token)) != 1 {
		http.Error(w, "Unauthorized", 401)
		return
	}
	switch {
	case r.URL.Path == "/api/frame" && r.Method == "GET":
		b, e := v.client.call("Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": 65})
		if e != nil {
			http.Error(w, "Browser unavailable", 503)
			return
		}
		var result struct {
			Data string `json:"data"`
		}
		if json.Unmarshal(b, &result) != nil {
			http.Error(w, "Invalid frame", 502)
			return
		}
		data, e := base64.StdEncoding.DecodeString(result.Data)
		if e != nil {
			http.Error(w, "Invalid frame", 502)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(data)
	case r.URL.Path == "/api/action" && r.Method == "POST":
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		var action struct {
			Action string  `json:"action"`
			URL    string  `json:"url"`
			Text   string  `json:"text"`
			X      float64 `json:"x"`
			Y      float64 `json:"y"`
			Key    string  `json:"key"`
		}
		if json.NewDecoder(r.Body).Decode(&action) != nil {
			http.Error(w, "Invalid request", 400)
			return
		}
		var e error
		switch action.Action {
		case "navigate":
			u, err := url.Parse(action.URL)
			if err != nil || u.Scheme != "http" && u.Scheme != "https" && action.URL != "about:blank" {
				http.Error(w, "Only web URLs are allowed", 400)
				return
			}
			_, e = v.client.call("Page.navigate", map[string]any{"url": action.URL})
		case "click":
			if action.X < 0 || action.Y < 0 || action.X > 10000 || action.Y > 10000 {
				http.Error(w, "Invalid coordinates", 400)
				return
			}
			for _, kind := range []string{"mousePressed", "mouseReleased"} {
				_, e = v.client.call("Input.dispatchMouseEvent", map[string]any{"type": kind, "x": action.X, "y": action.Y, "button": "left", "clickCount": 1})
				if e != nil {
					break
				}
			}
		case "text":
			_, e = v.client.call("Input.insertText", map[string]any{"text": action.Text})
		case "key":
			if action.Key != "Enter" && action.Key != "Tab" && action.Key != "Backspace" && action.Key != "Escape" {
				http.Error(w, "Unsupported key", 400)
				return
			}
			for _, kind := range []string{"keyDown", "keyUp"} {
				_, e = v.client.call("Input.dispatchKeyEvent", map[string]any{"type": kind, "key": action.Key})
				if e != nil {
					break
				}
			}
		case "reload":
			_, e = v.client.call("Page.reload", map[string]any{})
		default:
			http.Error(w, "Unsupported action", 400)
			return
		}
		if e != nil {
			http.Error(w, "Browser action failed", 502)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	default:
		http.NotFound(w, r)
	}
}
func (a *App) browser(args []string) error {
	c, e := a.load()
	if e != nil {
		return e
	}
	if len(args) > 0 && args[0] == "mcp" {
		return interactive("ssh", append(a.sshArgs(c, false), shellJoin([]string{c.RemoteBinary, "remote", "browser-mcp", c.RemoteRoot})))
	}
	cmd := exec.Command("ssh", append(a.sshArgs(c, false), shellJoin([]string{c.RemoteBinary, "remote", "browser", c.RemoteRoot}))...)
	keepalive, e := cmd.StdinPipe()
	if e != nil {
		return e
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		return e
	}
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		return e
	}
	defer func() { keepalive.Close(); cmd.Process.Kill(); cmd.Wait() }()
	scanner := bufio.NewScanner(out)
	var state browserInfo
	var lines strings.Builder
	for scanner.Scan() {
		lines.WriteString(scanner.Text())
		lines.WriteByte('\n')
		if json.Unmarshal([]byte(lines.String()), &state) == nil && state.Port > 0 {
			break
		}
	}
	if state.Port == 0 {
		return errors.New("远端浏览器没有启动；请查看提示或 tools install browser")
	}
	local, e := freePort()
	if e != nil {
		return e
	}
	opts := []string{"-N", "-o", "ExitOnForwardFailure=yes", "-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", local, state.Port)}
	tunnel := exec.Command("ssh", sshOptions(a.sshArgs(c, false), opts)...)
	tunnel.Stderr = os.Stderr
	if e = tunnel.Start(); e != nil {
		return e
	}
	defer func() { tunnel.Process.Kill(); tunnel.Wait() }()
	address := fmt.Sprintf("http://127.0.0.1:%d/#%s", local, state.Token)
	for i := 0; i < 100; i++ {
		r, e := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", local))
		if e == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Println("开发浏览器已就绪；保持此命令运行，Ctrl+C 结束。地址只用于本次授权会话：")
	fmt.Println(address)
	_ = openBrowser(address)
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	<-ctx.Done()
	return nil
}
func browserMCP(root string) error {
	if _, e := exec.LookPath("npx"); e != nil {
		return errors.New("需要 Node LTS：运行 rdev tools install node")
	}
	b, e := os.ReadFile(filepath.Join(root, ".browser", "state.json"))
	if e != nil {
		return errors.New("先运行 rdev browser")
	}
	var state browserInfo
	if json.Unmarshal(b, &state) != nil || state.CDPPort < 1 {
		return errors.New("无效浏览器状态")
	}
	os.Setenv("CHROME_DEVTOOLS_MCP_NO_UPDATE_CHECKS", "1")
	os.Setenv("NPM_CONFIG_UPDATE_NOTIFIER", "false")
	return interactive("npx", []string{"--yes", "chrome-devtools-mcp@1.13.0", "--no-usage-statistics", "--no-performance-crux", "--browser-url", fmt.Sprintf("http://127.0.0.1:%d", state.CDPPort)})
}

const browserHTML = `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>远端开发浏览器</title><style>body{margin:0;background:#f4f3ef;color:#292b29;font:15px system-ui}header{display:flex;align-items:center;gap:10px;padding:12px 20px;border-bottom:1px solid #d4d5cf;background:white}input{flex:1;padding:9px;border:1px solid #bbb;border-radius:5px}button{padding:9px 13px;border:1px solid #bbb;background:#fff;border-radius:5px;cursor:pointer}main{padding:16px}img{display:block;max-width:100%;background:#fff;border:1px solid #ccc}footer{padding:0 20px 12px;color:#646962}</style><header><strong>远端开发浏览器</strong><input id="url" aria-label="网页地址" placeholder="https:// 或远端 http://localhost:端口"><button id="go">打开</button><button id="reload">刷新</button><button id="type">输入文字</button></header><main><img id="screen" tabindex="0" alt="远端浏览器画面"></main><footer id="status">正在连接远端浏览器…</footer><script>
const token=location.hash.slice(1);history.replaceState(null,'',location.pathname);const headers={'Authorization':'Bearer '+token};let busy=false;
async function action(value){const r=await fetch('/api/action',{method:'POST',headers:{...headers,'Content-Type':'application/json'},body:JSON.stringify(value)});document.querySelector('#status').textContent=r.ok?'浏览器运行在远端；网页请求使用远端网络出口':'操作失败：'+r.status;await frame()}
async function frame(){if(busy)return;busy=true;try{const r=await fetch('/api/frame',{headers});if(!r.ok)throw Error(r.status);const old=document.querySelector('#screen').src;document.querySelector('#screen').src=URL.createObjectURL(await r.blob());if(old.startsWith('blob:'))URL.revokeObjectURL(old)}catch(e){document.querySelector('#status').textContent='连接不可用：'+e.message}finally{busy=false}}
document.querySelector('#go').onclick=()=>action({action:'navigate',url:document.querySelector('#url').value});document.querySelector('#url').onkeydown=e=>{if(e.key==='Enter')document.querySelector('#go').click()};document.querySelector('#reload').onclick=()=>action({action:'reload'});document.querySelector('#type').onclick=()=>{const text=prompt('输入到远端当前选中的输入框');if(text!==null)action({action:'text',text})};document.querySelector('#screen').onclick=e=>{const i=e.target,r=i.getBoundingClientRect();action({action:'click',x:(e.clientX-r.left)*i.naturalWidth/r.width,y:(e.clientY-r.top)*i.naturalHeight/r.height});i.focus()};document.querySelector('#screen').onkeydown=e=>{if(['Enter','Tab','Backspace','Escape'].includes(e.key)){e.preventDefault();action({action:'key',key:e.key})}};frame();setInterval(frame,1200);
</script></html>`

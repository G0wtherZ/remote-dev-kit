package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func issueCode(err error) string {
	var e *operationError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestHelperCompatibilityUsesProtocolAndRequestedCapabilities(t *testing.T) {
	info := localHelperInfo()
	info.HelperVersion = "9.9.9"
	if err := validateHelper(info, capShare, capMountHealth); err != nil {
		t.Fatal("compatible releases need not match", err)
	}
	info.Capabilities = []string{capTerminal}
	if err := validateHelper(info, capTerminal); err != nil {
		t.Fatal(err)
	}
	if code := issueCode(validateHelper(info, capShare)); code != "helper_capability_missing" {
		t.Fatal(code)
	}
	info.ProtocolVersion = helperProtocol + 1
	if code := issueCode(validateHelper(info)); code != "helper_protocol_mismatch" {
		t.Fatal(code)
	}
	if code := issueCode(validateHelper(HelperInfo{HelperVersion: "0.1.0-alpha.3"})); code != "helper_protocol_missing" {
		t.Fatal(code)
	}
	for _, bad := range []HelperInfo{
		{HelperVersion: "bad\nlegacy-version"},
		{HelperVersion: "bad\nversion", ProtocolVersion: 1, Capabilities: []string{}},
		{HelperVersion: "1.0.0", ProtocolVersion: 1},
		{HelperVersion: "1.0.0", ProtocolVersion: 1, Capabilities: []string{"shell;injection"}},
	} {
		if issueCode(validateHelper(bad)) != "helper_metadata_invalid" {
			t.Fatal("accepted malformed metadata", bad)
		}
	}
}

func preflightFixture(t *testing.T) (*App, Config, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX SSH transport fixture")
	}
	h := t.TempDir()
	tools := filepath.Join(h, "tools")
	os.Mkdir(tools, 0700)
	ssh := "#!/bin/sh\nfor arg do last=$arg; done\nprintf '%s\\n' \"$last\" >> " + shellQuote(filepath.Join(h, "ssh.log")) + "\nexec /bin/sh -c \"$last\"\n"
	os.WriteFile(filepath.Join(tools, "ssh"), []byte(ssh), 0700)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := Config{Version: 1, Name: "preflight", AdminSSH: "fixture", BootstrapBinary: filepath.Join(h, "rdev"), RemoteBinary: filepath.Join(h, "rdev"), RemoteRoot: filepath.Join(h, "remote"), LocalRoot: filepath.Join(h, "share"), RemoteIP: "100.64.1.2", Tailscale: filepath.Join(tools, "tailscale")}
	os.Mkdir(c.LocalRoot, 0700)
	os.WriteFile(c.Tailscale, []byte("#!/bin/sh\nprintf '%s' '{\"BackendState\":\"Running\"}'\n"), 0700)
	a := &App{State: filepath.Join(h, "private")}
	if e := a.save(c); e != nil {
		t.Fatal(e)
	}
	info := localHelperInfo()
	b, _ := json.Marshal(info)
	os.WriteFile(filepath.Join(h, "metadata.json"), b, 0600)
	probe := RemoteInfo{HelperInfo: info, OS: runtime.GOOS, Arch: runtime.GOARCH, UID: 501, Tools: map[string]string{"tmux": "fixture"}}
	b, _ = json.Marshal(probe)
	os.WriteFile(filepath.Join(h, "probe.json"), b, 0600)
	script := "#!/bin/sh\ncase \"$1:$2\" in\n" +
		"remote:handshake) cat " + shellQuote(filepath.Join(h, "metadata.json")) + ";;\n" +
		"remote:probe) cat " + shellQuote(filepath.Join(h, "probe.json")) + ";;\n" +
		"--version:) printf 'rdev 0.1.0-alpha.3\\n';;\n" +
		"*) printf '%s' \"$2\" >> " + shellQuote(filepath.Join(h, "actions.log")) + ";;\nesac\n"
	os.WriteFile(c.RemoteBinary, []byte(script), 0700)
	return a, c, h
}

func TestIncompatibleHelpersBlockEntryPointsWithoutChangingAuthorization(t *testing.T) {
	for _, mode := range []string{"future", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			a, c, h := preflightFixture(t)
			c.AutoShare = true
			if e := a.save(c); e != nil {
				t.Fatal(e)
			}
			info := localHelperInfo()
			info.ProtocolVersion++
			b, _ := json.Marshal(info)
			expected := "helper_protocol_mismatch"
			if mode == "malformed" {
				b = []byte("not-json")
				expected = "helper_metadata_invalid"
			}
			os.WriteFile(filepath.Join(h, "metadata.json"), b, 0600)
			before, _ := os.ReadFile(filepath.Join(a.State, "config.json"))
			commands := []func() error{
				func() error { return a.terminal("claude", nil) }, func() error { return a.terminal("shell", nil) },
				func() error { return a.reconnect(nil) }, func() error { return a.share([]string{"on"}) },
				func() error { return a.expose([]string{"--yes", "12345"}) }, func() error { return a.browser(nil) },
				func() error { return a.browser([]string{"mcp"}) }, func() error { return a.agent([]string{"install"}) },
				func() error { return a.tools([]string{"install", "base"}) }, func() error { return a.tools([]string{"install", "node"}) },
			}
			for i, command := range commands {
				if e := command(); issueCode(e) != expected {
					t.Fatalf("entry point %d: %v", i, e)
				}
			}
			after, _ := os.ReadFile(filepath.Join(a.State, "config.json"))
			if string(before) != string(after) {
				t.Fatal("failed precheck changed configuration")
			}
			if _, e := os.Stat(filepath.Join(h, "actions.log")); !os.IsNotExist(e) {
				t.Fatal("an operation reached the incompatible helper")
			}
		})
	}
}

func TestDoctorSeparatesDependencyFailureAndDisabledOptionalCapability(t *testing.T) {
	a, c, h := preflightFixture(t)
	c.AutoShare = true
	a.save(c)
	text, e := captureStdout(t, func() error { return a.doctor([]string{"--json"}) })
	if e == nil {
		t.Fatal("missing SSHFS should be reported")
	}
	if !strings.Contains(text, "dependency_missing") || strings.Contains(text, "helper_capability_missing") || strings.Contains(text, "share_unreadable") {
		t.Fatal(text)
	}
	// A helper that only supports terminals remains usable when sharing is off.
	info := localHelperInfo()
	info.Capabilities = []string{capTerminal}
	b, _ := json.Marshal(info)
	os.WriteFile(filepath.Join(h, "metadata.json"), b, 0600)
	probe := RemoteInfo{HelperInfo: info, OS: runtime.GOOS, UID: 501, Tools: map[string]string{"tmux": "fixture"}}
	b, _ = json.Marshal(probe)
	os.WriteFile(filepath.Join(h, "probe.json"), b, 0600)
	c.AutoShare = false
	a.save(c)
	a.helperCache = nil
	if text, e = captureStdout(t, func() error { return a.doctor([]string{"--json"}) }); e != nil {
		t.Fatal(text, e)
	}
}

func TestCompatibilityCacheExpiresWithoutPersistingPermissions(t *testing.T) {
	a, c, h := preflightFixture(t)
	if _, e := a.preflight(c, capTerminal); e != nil {
		t.Fatal(e)
	}
	a.helperCache.checkedAt = time.Now().Add(-time.Minute)
	info := localHelperInfo()
	info.ProtocolVersion++
	b, _ := json.Marshal(info)
	os.WriteFile(filepath.Join(h, "metadata.json"), b, 0600)
	if _, e := a.preflight(c, capTerminal); issueCode(e) != "helper_protocol_mismatch" {
		t.Fatal("stale compatibility reused", e)
	}
	log, _ := os.ReadFile(filepath.Join(h, "ssh.log"))
	if strings.Count(string(log), "handshake") != 2 {
		t.Fatal(string(log))
	}
}

func captureStdout(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	err := f()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	r.Close()
	return string(b), err
}

func TestMissingCapabilityBlocksEffectsButAllowsCleanup(t *testing.T) {
	a, c, h := preflightFixture(t)
	info := localHelperInfo()
	info.Capabilities = []string{capTerminal}
	b, _ := json.Marshal(info)
	os.WriteFile(filepath.Join(h, "metadata.json"), b, 0600)
	if _, e := a.remote(c, []string{"port-prepare", c.RemoteRoot}, nil); issueCode(e) != "helper_capability_missing" {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(h, "actions.log")); !os.IsNotExist(e) {
		t.Fatal("operation ran before its capability check")
	}
	if _, e := a.preflight(c, capTerminal); e != nil {
		t.Fatal(e)
	}
	log, _ := os.ReadFile(filepath.Join(h, "ssh.log"))
	if strings.Count(string(log), "handshake") != 1 {
		t.Fatal("metadata was not reused within the same command")
	}
	if _, e := a.remote(c, []string{"unmount", c.RemoteRoot}, nil); e != nil {
		t.Fatal("cleanup was blocked", e)
	}
	b, _ = os.ReadFile(filepath.Join(h, "actions.log"))
	if string(b) != "unmount" {
		t.Fatal(string(b))
	}
}

func TestLegacyHelperReportsOwnVersionWithoutMountDiagnosis(t *testing.T) {
	a, c, h := preflightFixture(t)
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf 'rdev 0.1.0-alpha.3\\n'; exit 0; fi\necho 'rdev: unknown remote operation' >&2\nexit 1\n"
	os.WriteFile(c.RemoteBinary, []byte(script), 0700)
	info, e := a.preflight(c, capShare)
	if issueCode(e) != "helper_protocol_missing" || info.HelperVersion != "0.1.0-alpha.3" {
		t.Fatal(info, e)
	}
	text, err := captureStdout(t, func() error { return a.doctor([]string{"--json"}) })
	if err == nil {
		t.Fatal("legacy helper should fail readiness")
	}
	var report healthReport
	if json.Unmarshal([]byte(text), &report) != nil {
		t.Fatal(text)
	}
	if report.HelperCompatible || !report.ConnectionReady || report.RemoteHelper.HelperVersion != "0.1.0-alpha.3" {
		t.Fatal(text)
	}
	if strings.Contains(text, "share_unreadable") || strings.Contains(text, "share_not_mounted") {
		t.Fatal("legacy helper mislabeled as a mount failure", text)
	}
	if !strings.Contains(text, "--state-dir") {
		t.Fatal("repair advice lost the current profile")
	}
	log, _ := os.ReadFile(filepath.Join(h, "ssh.log"))
	if strings.Contains(string(log), "'probe'") {
		t.Fatal("runtime diagnosis proceeded past incompatible metadata")
	}
}

func TestWrongHelperPathIsNotExecuted(t *testing.T) {
	a, c, h := preflightFixture(t)
	c.RemoteBinary = filepath.Join(h, "claude")
	os.WriteFile(c.RemoteBinary, []byte("#!/bin/sh\ntouch "+shellQuote(filepath.Join(h, "claude-invoked"))+"\n"), 0700)
	if _, e := a.preflight(c); issueCode(e) != "helper_path_invalid" {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(h, "claude-invoked")); !os.IsNotExist(e) {
		t.Fatal("Claude was invoked")
	}
	if _, e := os.Stat(filepath.Join(h, "ssh.log")); !os.IsNotExist(e) {
		t.Fatal("invalid helper path reached SSH")
	}
}

func TestRemoteHandshakeDoesNotAccessWorkspaceOrClaude(t *testing.T) {
	h := t.TempDir()
	tools := filepath.Join(h, "bin")
	os.Mkdir(tools, 0700)
	marker := filepath.Join(h, "claude-invoked")
	os.WriteFile(filepath.Join(tools, "claude"), []byte("#!/bin/sh\ntouch "+shellQuote(marker)+"\n"), 0700)
	t.Setenv("HOME", h)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	text, e := captureStdout(t, func() error { return remoteCommand([]string{"handshake", "/"}) })
	if e != nil {
		t.Fatal(e)
	}
	var info HelperInfo
	if json.Unmarshal([]byte(text), &info) != nil || validateHelper(info, capShare) != nil {
		t.Fatal(text)
	}
	if _, e := os.Stat(filepath.Join(h, "RemoteDev")); !os.IsNotExist(e) {
		t.Fatal("handshake created a workspace")
	}
	if _, e := os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("handshake executed Claude")
	}
}

func TestConnectionFailuresHaveDistinctCodes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX exit fixture")
	}
	a := &App{State: t.TempDir()}
	err := exec.Command("sh", "-c", "exit 255").Run()
	for _, test := range []struct{ stderr, code string }{
		{"Host key verification failed.", "host_key_mismatch"},
		{"Permission denied (publickey).", "ssh_authentication_failed"},
		{"private-network destination is unavailable", "connection_unavailable"},
	} {
		if got := issueCode(a.classifyRemoteError(err, test.stderr, false)); got != test.code {
			t.Fatal(got, test)
		}
	}
	if got := issueCode(a.classifyRemoteError(err, "", true)); got != "connection_timeout" {
		t.Fatal(got)
	}
	for _, test := range []struct{ exit, code string }{{"127", "helper_missing"}, {"126", "helper_not_executable"}} {
		err = exec.Command("sh", "-c", "exit "+test.exit).Run()
		if got := issueCode(a.classifyRemoteError(err, "not found", false)); got != test.code {
			t.Fatal(got)
		}
	}
}

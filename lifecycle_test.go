package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPinnedKeyCannotBeSilentlyRotated(t *testing.T) {
	file := filepath.Join(t.TempDir(), "known_hosts")
	os.WriteFile(file, []byte("rdev-test ssh-ed25519 SAME old-comment\n"), 0600)
	if e := verifyPinnedKey(file, "ssh-ed25519 SAME new-comment"); e != nil {
		t.Fatal(e)
	}
	if verifyPinnedKey(file, "ssh-ed25519 DIFFERENT") == nil {
		t.Fatal("accepted changed identity")
	}
	if verifyPinnedKey(file, "") == nil {
		t.Fatal("accepted missing identity")
	}
}

func TestInterruptedAccountSetupReusesItsIdentity(t *testing.T) {
	a, c, h := lifecycleFixture(t)
	c.Identity = filepath.Join(a.State, "identity_ed25519")
	c.UserProvisionPending = true
	os.MkdirAll(filepath.Dir(c.BootstrapBinary), 0700)
	response, _ := json.Marshal(map[string]string{"user": "rdev", "binary": c.RemoteBinary})
	script := "#!/bin/sh\ncat >/dev/null\nprintf %s " + shellQuote(string(response)) + "\n"
	os.WriteFile(c.BootstrapBinary, []byte(script), 0700)
	if e := a.provisionUser(&c); e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(c.Identity)
	os.Remove(c.Identity + ".pub") // Interrupted state can be repaired from its private key.
	if e := a.provisionUser(&c); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(c.Identity)
	if string(before) != string(after) || c.UserProvisionPending {
		t.Fatal("identity replaced")
	}
	alternate := filepath.Join(h, "alternate")
	if e := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", alternate).Run(); e != nil {
		t.Fatal(e)
	}
	bad, _ := os.ReadFile(alternate + ".pub")
	os.WriteFile(c.Identity+".pub", bad, 0600)
	if a.provisionUser(&c) == nil {
		t.Fatal("accepted mismatched key pair")
	}
}

// A local SSH transport fixture executes our quoted remote commands and uploader,
// so preservation is checked against real files without contacting a server.
func lifecycleFixture(t *testing.T) (*App, Config, string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("local launchd lifecycle fixture; remote behavior also tested on Linux VPS")
	}
	h := t.TempDir()
	tools := filepath.Join(h, "tools")
	os.Mkdir(tools, 0700)
	ssh := `#!/bin/sh
for arg do
 if [ "$arg" = -G ]; then printf 'user dev\nport 22\nidentityfile none\n'; exit 0; fi
 last=$arg
done
exec /bin/sh -c "$last"
`
	os.WriteFile(filepath.Join(tools, "ssh"), []byte(ssh), 0700)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := filepath.Join(h, "Cloud Work 资料")
	local := filepath.Join(h, "Local Share")
	os.MkdirAll(filepath.Join(root, "work"), 0700)
	os.Mkdir(local, 0700)
	a := &App{State: filepath.Join(h, "private")}
	c := Config{Version: 1, Name: "test", AdminSSH: "fixture", LocalRoot: local, RemoteRoot: root, RemoteBinary: filepath.Join(h, "helpers", "rdev"), BootstrapBinary: filepath.Join(h, "helpers", "rdev"), RemoteOS: runtime.GOOS, RemoteArch: runtime.GOARCH, RemoteIP: "100.64.1.2", NetworkMode: "existing", Tailscale: filepath.Join(tools, "tailscale")}
	os.WriteFile(c.Tailscale, []byte("#!/bin/sh\nprintf '%s' '{\"BackendState\":\"Running\"}'\n"), 0700)
	info := RemoteInfo{OS: runtime.GOOS, Arch: runtime.GOARCH, UID: 501, Root: root, HostKey: "ssh-ed25519 SAME fixture", IPs: []string{c.RemoteIP}, NetworkState: "Running", Tools: map[string]string{"tmux": "/fixture/tmux"}}
	b, _ := json.Marshal(info)
	helper := "#!/bin/sh\ncase \"$2\" in probe|init) printf %s " + shellQuote(string(b)) + ";; unmount) exit 0;; *) exit 1;; esac\n"
	assets := filepath.Join(h, "assets")
	target := filepath.Join(assets, "rdev", c.RemoteOS+"-"+c.RemoteArch)
	os.MkdirAll(target, 0700)
	os.WriteFile(filepath.Join(target, "rdev"), []byte(helper), 0700)
	t.Setenv("RDEV_ASSETS", assets)
	if e := a.save(c); e != nil {
		t.Fatal(e)
	}
	return a, c, h
}

func TestResetRebuildsHelperWithoutTouchingAgentData(t *testing.T) {
	a, c, h := lifecycleFixture(t)
	if e := uploadRemoteHelper(c); e != nil {
		t.Fatal(e)
	}
	sentinels := []string{filepath.Join(h, ".claude", "history.jsonl"), filepath.Join(h, ".claude.json"), filepath.Join(c.RemoteRoot, "work", "important.txt"), filepath.Join(c.LocalRoot, "local.txt"), filepath.Join(a.State, "identity_ed25519")}
	for _, p := range sentinels {
		os.MkdirAll(filepath.Dir(p), 0700)
		os.WriteFile(p, []byte("preserve-me"), 0600)
	}
	os.WriteFile(c.RemoteBinary, []byte("#!/bin/sh\nexit 99\n"), 0700)
	if e := a.reset([]string{"--yes"}); e != nil {
		t.Fatal(e)
	}
	for _, p := range sentinels {
		b, e := os.ReadFile(p)
		if e != nil || string(b) != "preserve-me" {
			t.Fatalf("data changed: %s", p)
		}
	}
	loaded, e := a.load()
	if e != nil {
		t.Fatal(e)
	}
	if loaded.AutoShare || loaded.SetupStage != "ready" {
		t.Fatal("reset changed authorization or stage")
	}
}

func TestResetRefusesUnownedHelperAndPlanDoesNotWrite(t *testing.T) {
	a, c, _ := lifecycleFixture(t)
	os.MkdirAll(filepath.Dir(c.RemoteBinary), 0700)
	os.WriteFile(c.RemoteBinary, []byte("unrelated-rdev-file"), 0700)
	before, _ := os.ReadFile(filepath.Join(a.State, "config.json"))
	if e := a.reset([]string{"--plan"}); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(filepath.Join(a.State, "config.json"))
	if string(before) != string(after) {
		t.Fatal("plan mutated configuration")
	}
	if a.reset([]string{"--yes"}) == nil {
		t.Fatal("overwrote unowned file")
	}
	b, _ := os.ReadFile(c.RemoteBinary)
	if string(b) != "unrelated-rdev-file" {
		t.Fatal("unrelated helper changed")
	}
}

func TestSetupResumeCompletesStageAndPreservesDisabledSharing(t *testing.T) {
	a, c, _ := lifecycleFixture(t)
	if e := uploadRemoteHelper(c); e != nil {
		t.Fatal(e)
	}
	c.SetupStage = "dependencies"
	a.save(c)
	if e := a.setup([]string{"--resume", "--yes"}); e != nil {
		t.Fatal(e)
	}
	got, _ := a.load()
	if got.SetupStage != "ready" || got.AutoShare {
		t.Fatal("resume failed or widened permission")
	}
}

func TestEditingPlanPreservesIdentityAndDoesNotMoveFiles(t *testing.T) {
	a, c, h := lifecycleFixture(t)
	next := filepath.Join(h, "new share")
	before, _ := os.ReadFile(filepath.Join(a.State, "config.json"))
	if e := a.setup([]string{"--edit", "--yes", "--plan", "--local-root", next, "--read-only"}); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(filepath.Join(a.State, "config.json"))
	if string(before) != string(after) {
		t.Fatal("plan changed identity")
	}
	if _, e := os.Stat(next); !os.IsNotExist(e) {
		t.Fatal("plan created new shared root")
	}
	if e := a.setup([]string{"--yes", "--ssh", "other-host"}); e == nil {
		t.Fatal("allowed host identity change in existing profile")
	}
	// A repeated interactive setup defaults to continuing the existing profile.
	if e := uploadRemoteHelper(c); e != nil {
		t.Fatal(e)
	}
	f := flag.NewFlagSet("setup", flag.ContinueOnError)
	if e := a.existingSetup(f, &Prompter{bufio.NewReader(strings.NewReader("\n"))}, false, false, false, false, "", "", false, false); e != nil {
		t.Fatal(e)
	}
}

func TestColdPrivateProbeRetriesButMutationDoesNot(t *testing.T) {
	a, c, h := lifecycleFixture(t)
	if e := uploadRemoteHelper(c); e != nil {
		t.Fatal(e)
	}
	gate := filepath.Join(h, "path-ready")
	transport := `#!/bin/sh
private=no
for arg do
 if [ "$arg" = -F ]; then private=yes; fi
 last=$arg
done
if [ "$private" = yes ] && [ ! -e ` + shellQuote(gate) + ` ]; then
 echo 'private-network destination is unavailable' >&2; exit 255
fi
exec /bin/sh -c "$last"
`
	os.WriteFile(filepath.Join(h, "tools", "ssh"), []byte(transport), 0700)
	ts := "#!/bin/sh\ncase \"$*\" in *ping*) touch " + shellQuote(gate) + ";; esac\nprintf '%s' '{\"BackendState\":\"Running\"}'\n"
	os.WriteFile(c.Tailscale, []byte(ts), 0700)
	if _, e := a.remote(c, []string{"port-prepare", c.RemoteRoot}, nil); e == nil {
		t.Fatal("mutation unexpectedly succeeded")
	}
	if _, e := os.Stat(gate); !os.IsNotExist(e) {
		t.Fatal("mutation was replayed or path warmed")
	}
	if _, e := a.remote(c, []string{"probe", c.RemoteRoot}, nil); e != nil {
		t.Fatal(e)
	}
	if transientPrivateFailure("exit status 255: Host key verification failed") {
		t.Fatal("identity errors must not be retried")
	}
}

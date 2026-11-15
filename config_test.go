package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShareRootDoesNotExposeHomeOrPrivateState(t *testing.T) {
	h := t.TempDir()
	state := filepath.Join(h, ".rdev")
	good := filepath.Join(h, "My research 资料")
	for _, p := range []string{h, filepath.Join(h, ".ssh"), filepath.Join(h, ".ssh", "keys"), state, filepath.Join(state, "child"), filepath.Dir(h)} {
		if validateRoot(p, h, state) == nil {
			t.Errorf("accepted unsafe root %q", p)
		}
	}
	if e := validateRoot(good, h, state); e != nil {
		t.Fatal(e)
	}
}
func TestShellArgumentsRemainData(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("POSIX remote command quoting")
	}
	for _, value := range []string{"a b", "quote'\"", "$(touch SHOULD_NOT_EXIST)", "semi; colon", "资料目录"} {
		b, e := exec.Command("sh", "-c", "printf '%s' "+shellQuote(value)).Output()
		if e != nil || string(b) != value {
			t.Fatalf("argument changed: %q, %q, %v", value, b, e)
		}
	}
}
func TestDestinationCannotInjectSSHOptions(t *testing.T) {
	for _, s := range []string{"-oProxyCommand=evil", "host -p 2222", "host\ncommand", ""} {
		if validDestination(s) {
			t.Fatal("accepted", s)
		}
	}
	for _, s := range []string{"server", "dev@example.org", "dev@100.64.1.2"} {
		if !validDestination(s) {
			t.Fatal("rejected", s)
		}
	}
}
func TestSetupPlanIsReadOnly(t *testing.T) {
	h := t.TempDir()
	a := &App{State: filepath.Join(h, "private")}
	root := filepath.Join(h, "custom root")
	if e := a.setup([]string{"--yes", "--plan", "--ssh", "dev@example.org", "--local-root", root, "--remote-root", "~/Custom Dev"}); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{root, a.State} {
		if _, e := os.Stat(p); !os.IsNotExist(e) {
			t.Fatal("plan created", p)
		}
	}
}
func TestSSHExtraOptionsPrecedeDestination(t *testing.T) {
	a := sshOptions([]string{"-T", "host"}, []string{"-N", "-L", "127.0.0.1:1:127.0.0.1:2"})
	if a[len(a)-1] != "host" || strings.Join(a, " ") != "-T -N -L 127.0.0.1:1:127.0.0.1:2 host" {
		t.Fatal(a)
	}
}

func TestSymlinkedParentCannotHideCredentialDirectory(t *testing.T) {
	h := t.TempDir()
	os.Mkdir(filepath.Join(h, ".ssh"), 0700)
	if e := os.Symlink(filepath.Join(h, ".ssh"), filepath.Join(h, "innocent")); e != nil {
		t.Skip(e)
	}
	resolved, e := resolveCreationPath(filepath.Join(h, "innocent", "new-share"))
	if e != nil {
		t.Fatal(e)
	}
	if validateRoot(resolved, h, filepath.Join(h, ".rdev")) == nil {
		t.Fatal("symlinked parent bypassed root validation")
	}
}
func TestPrivateStateDoesNotClobberUnrelatedDirectory(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "config.json")
	os.WriteFile(sentinel, []byte("unrelated-app"), 0600)
	a := &App{State: dir}
	if a.save(Config{Version: 1}) == nil {
		t.Fatal("overwrote unrelated state")
	}
	b, _ := os.ReadFile(sentinel)
	if string(b) != "unrelated-app" {
		t.Fatal("unrelated data changed")
	}
}
func TestWindowsPipeAndTaskArguments(t *testing.T) {
	b := string(rune(92))
	want := b + b + "." + b + "pipe" + b + "rdev-test"
	if windowsPipe("test") != want {
		t.Fatal("wrong Windows pipe")
	}
	for _, test := range []struct{ input, want string }{
		{"plain", "plain"}, {"", "\"\""}, {"a b", "\"a b\""},
		{"C:" + b + "Users" + b + "Me", "C:" + b + "Users" + b + "Me"},
		{"C:" + b + "User Name" + b, "\"C:" + b + "User Name" + b + b + "\""},
	} {
		if got := windowsQuote(test.input); got != test.want {
			t.Fatalf("Windows quoting %q: %q, want %q", test.input, got, test.want)
		}
	}
}

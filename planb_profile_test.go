package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bFixtureProfile(t *testing.T) (*App, bProfile) {
	t.Helper()
	a := &App{State: filepath.Join(t.TempDir(), "state")}
	if e := os.MkdirAll(a.bDir(), 0700); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(a.bDir(), ".native-owner"), []byte("remote-dev-kit-native-b\n"), 0600)
	id := "0123456789ab"
	c := bProfile{Protocol: bProtocol, ServerID: id, DeviceID: "abcdef012345", Container: "rdev-b-" + id, Image: "rdev-native-b:0123456789abcdef", RemoteRoot: "/home/example/.rdev-b/" + id, SSH: "private-example", ControlPort: 23456, WebPort: 18788, ControlToken: strings.Repeat("a", 64)}
	if e := a.bSave(c); e != nil {
		t.Fatal(e)
	}
	return a, c
}
func TestBProfileCannotImportUnownedState(t *testing.T) {
	a, _ := bFixtureProfile(t)
	os.Remove(filepath.Join(a.bDir(), ".native-owner"))
	if _, e := a.bLoad(); e == nil {
		t.Fatal("accepted unowned state")
	}
}
func TestBProfilePreservesIdentityAndRejectsIncompatibleProtocol(t *testing.T) {
	a, c := bFixtureProfile(t)
	got, e := a.bLoad()
	if e != nil || got.DeviceID != c.DeviceID || got.ServerID != c.ServerID {
		t.Fatal(e)
	}
	c.Protocol++
	a.bSave(c)
	if _, e := a.bLoad(); e == nil {
		t.Fatal("accepted incompatible protocol")
	}
}
func TestBOnRequiresExplicitLocalExecution(t *testing.T) {
	a, _ := bFixtureProfile(t)
	if e := a.planB([]string{"on"}); e == nil || !strings.Contains(e.Error(), "--trust-local-user") {
		t.Fatalf("missing trust opt-in: %v", e)
	}
}

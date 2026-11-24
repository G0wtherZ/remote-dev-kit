package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestBTLSRejectsUnknownLoopbackEndpoint(t *testing.T) {
	dir := t.TempDir()
	if e := bEnsureTLSAt(dir); e != nil {
		t.Fatal(e)
	}
	cert, e := os.ReadFile(filepath.Join(dir, "device.crt"))
	if e != nil {
		t.Fatal(e)
	}
	pair, e := tls.LoadX509KeyPair(filepath.Join(dir, "device.crt"), filepath.Join(dir, "device.key"))
	if e != nil {
		t.Fatal(e)
	}
	trusted := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("trusted")) }))
	trusted.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}
	trusted.StartTLS()
	defer trusted.Close()
	config, e := bTLSConfig(bProfile{Certificate: string(cert)})
	if e != nil {
		t.Fatal(e)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil, TLSClientConfig: config}}
	defer client.CloseIdleConnections()
	response, e := client.Get(trusted.URL)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	unknown := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("untrusted endpoint received authenticated request")
	}))
	defer unknown.Close()
	if response, e = client.Get(unknown.URL); e == nil {
		response.Body.Close()
		t.Fatal("accepted wrong backend identity")
	}
	before := string(cert)
	if e = bEnsureTLSAt(dir); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "device.crt"))
	if before != string(after) {
		t.Fatal("rotated backend identity during repeat setup")
	}
}

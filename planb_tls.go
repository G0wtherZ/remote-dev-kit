package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

func bEnsureTLS() error {
	return bEnsureTLSAt("/data/config")
}
func bEnsureTLSAt(directory string) error {
	certFile, keyFile := filepath.Join(directory, "device.crt"), filepath.Join(directory, "device.key")
	if _, e := os.Stat(certFile); e == nil {
		_, e = tls.LoadX509KeyPair(certFile, keyFile)
		return e
	}
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return e
	}
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if e != nil {
		return e
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "rdev-device"}, DNSNames: []string{"rdev-device"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		return e
	}
	private, e := x509.MarshalECPrivateKey(key)
	if e != nil {
		return e
	}
	if e = atomicWrite(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}), 0600); e != nil {
		return e
	}
	return atomicWrite(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
}
func bTLSConfig(c bProfile) (*tls.Config, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(c.Certificate)) {
		return nil, errors.New("B 缺少由 SSH 验证的后端证书；重新 setup 后导出连接配置")
	}
	return &tls.Config{RootCAs: pool, ServerName: "rdev-device", MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}, nil
}

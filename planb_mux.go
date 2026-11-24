package main

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// The rdev orchestration is independent; reliable byte-stream multiplexing is
// delegated to the standard, pinned Yamux library instead of a custom TCP stack.
type bMux struct {
	ctx     context.Context
	cancel  context.CancelFunc
	raw     net.Conn
	session *yamux.Session
	dial    func() (net.Conn, error)
	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	closed  bool
	err     error
	workers sync.WaitGroup
}

func bNewMux(ctx context.Context, raw net.Conn, dial func() (net.Conn, error)) *bMux {
	ctx, cancel := context.WithCancel(ctx)
	m := &bMux{ctx: ctx, cancel: cancel, raw: raw, dial: dial, conns: map[net.Conn]struct{}{}}
	config := yamux.DefaultConfig()
	config.AcceptBacklog = 32
	config.KeepAliveInterval = 20 * time.Second
	config.ConnectionWriteTimeout = 45 * time.Second
	config.StreamOpenTimeout = 30 * time.Second
	config.MaxStreamWindowSize = 1024 * 1024
	config.LogOutput = io.Discard
	if dial == nil {
		m.session, m.err = yamux.Client(raw, config)
	} else {
		m.session, m.err = yamux.Server(raw, config)
	}
	m.workers.Add(1)
	return m
}
func (m *bMux) Open(conn net.Conn) error {
	if m.err != nil {
		conn.Close()
		return m.err
	}
	stream, e := m.session.OpenStream()
	if e != nil {
		conn.Close()
		return e
	}
	return m.pipe(conn, stream)
}
func (m *bMux) pipe(conn net.Conn, stream *yamux.Stream) error {
	m.mu.Lock()
	if m.closed || len(m.conns) >= 32 {
		m.mu.Unlock()
		conn.Close()
		stream.Close()
		return errors.New("device stream limit reached")
	}
	m.conns[conn] = struct{}{}
	m.workers.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.workers.Done()
		defer conn.Close()
		defer stream.Close()
		defer func() { m.mu.Lock(); delete(m.conns, conn); m.mu.Unlock() }()
		copied := make(chan struct{})
		go func() { io.Copy(stream, conn); stream.Close(); close(copied) }()
		io.Copy(conn, stream)
		if half, ok := conn.(interface{ CloseWrite() error }); ok {
			half.CloseWrite()
		} else {
			conn.Close()
		}
		<-copied
	}()
	return nil
}
func (m *bMux) Run() error {
	defer m.workers.Done()
	defer m.Close()
	if m.err != nil {
		return m.err
	}
	go func() { <-m.ctx.Done(); m.Close() }()
	if m.dial == nil {
		select {
		case <-m.ctx.Done():
			return m.ctx.Err()
		case <-m.session.CloseChan():
			return errors.New("device transport closed")
		}
	}
	for {
		stream, e := m.session.AcceptStream()
		if e != nil {
			return e
		}
		conn, e := m.dial()
		if e != nil {
			stream.Close()
			continue
		}
		if e = m.pipe(conn, stream); e != nil {
			stream.Close()
		}
	}
}
func (m *bMux) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	list := make([]net.Conn, 0, len(m.conns))
	for conn := range m.conns {
		list = append(list, conn)
	}
	m.mu.Unlock()
	m.cancel()
	m.raw.Close()
	if m.session != nil {
		m.session.Close()
	}
	for _, conn := range list {
		conn.Close()
	}
}

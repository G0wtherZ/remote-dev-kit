package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"github.com/coder/websocket"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// bMux is rdev's independent device-stream protocol. No upstream application
// protocol is used: five-byte headers, 16 KiB data frames, per-stream ACK windows.
const (
	bMuxOpen  byte = 1
	bMuxData  byte = 2
	bMuxClose byte = 3
	bMuxAck   byte = 4
	bMuxEOF   byte = 5
)

type bMux struct {
	ctx     context.Context
	cancel  context.CancelFunc
	ws      *websocket.Conn
	dial    func() (net.Conn, error)
	mu      sync.Mutex
	write   sync.Mutex
	streams map[uint32]*bMuxStream
	next    atomic.Uint32
	closed  bool
	workers sync.WaitGroup
}
type bMuxStream struct {
	peer       *bMux
	id         uint32
	conn       net.Conn
	queue      chan []byte
	done       chan struct{}
	once       sync.Once
	pending    atomic.Int64
	frames     atomic.Int32
	wake       chan struct{}
	readEnded  atomic.Bool
	writeEnded atomic.Bool
	remoteEOF  atomic.Bool
}

func bNewMux(ctx context.Context, ws *websocket.Conn, dial func() (net.Conn, error)) *bMux {
	ctx, cancel := context.WithCancel(ctx)
	m := &bMux{ctx: ctx, cancel: cancel, ws: ws, dial: dial, streams: map[uint32]*bMuxStream{}}
	m.workers.Add(1)
	ws.SetReadLimit(65536)
	return m
}
func (m *bMux) send(kind byte, id uint32, data []byte) error {
	packet := make([]byte, 5+len(data))
	packet[0] = kind
	binary.BigEndian.PutUint32(packet[1:5], id)
	copy(packet[5:], data)
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()
	m.write.Lock()
	defer m.write.Unlock()
	e := m.ws.Write(ctx, websocket.MessageBinary, packet)
	if e != nil {
		go m.Close()
	}
	return e
}
func (m *bMux) add(id uint32, conn net.Conn) (*bMuxStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || id == 0 || m.streams[id] != nil || len(m.streams) >= 32 {
		return nil, errors.New("device stream limit or duplicate ID")
	}
	s := &bMuxStream{peer: m, id: id, conn: conn, queue: make(chan []byte, 65), done: make(chan struct{}), wake: make(chan struct{}, 1)}
	m.streams[id] = s
	m.workers.Add(2)
	return s, nil
}
func (m *bMux) Open(conn net.Conn) error {
	id := m.next.Add(1)
	s, e := m.add(id, conn)
	if e != nil {
		conn.Close()
		return e
	}
	if e = m.send(bMuxOpen, id, nil); e != nil {
		s.stop(false)
		m.workers.Done()
		m.workers.Done()
		return e
	}
	go s.read()
	go s.write()
	return nil
}
func (m *bMux) get(id uint32) *bMuxStream { m.mu.Lock(); defer m.mu.Unlock(); return m.streams[id] }
func (m *bMux) Run() error {
	defer m.workers.Done()
	defer m.Close()
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
				e := m.ws.Ping(ctx)
				cancel()
				if e != nil {
					m.Close()
					return
				}
			}
		}
	}()
	for {
		kind, packet, e := m.ws.Read(m.ctx)
		if e != nil {
			return e
		}
		if kind != websocket.MessageBinary || len(packet) < 5 {
			return errors.New("invalid device frame")
		}
		id := binary.BigEndian.Uint32(packet[1:5])
		data := packet[5:]
		if id == 0 {
			return errors.New("invalid device stream ID")
		}
		switch packet[0] {
		case bMuxOpen:
			if m.dial == nil || len(data) != 0 {
				return errors.New("unexpected stream open")
			}
			// Dialing is local loopback only; it never targets an address selected by the peer.
			conn, e := m.dial()
			if e != nil {
				_ = m.send(bMuxClose, id, nil)
				continue
			}
			s, e := m.add(id, conn)
			if e != nil {
				conn.Close()
				return e
			}
			go s.read()
			go s.write()
		case bMuxData:
			if len(data) == 0 || len(data) > 16384 {
				return errors.New("oversized device data frame")
			}
			if s := m.get(id); s != nil {
				if s.remoteEOF.Load() {
					return errors.New("data after stream EOF")
				}
				copyData := append([]byte{}, data...)
				select {
				case <-s.done:
				case s.queue <- copyData:
				default:
					return errors.New("device stream exceeded receive window")
				}
			}
		case bMuxAck:
			if len(data) != 4 {
				return errors.New("invalid device ACK")
			}
			if s := m.get(id); s != nil {
				n := int64(binary.BigEndian.Uint32(data))
				if n <= 0 || n > s.pending.Load() || s.frames.Load() <= 0 {
					return errors.New("invalid device ACK count")
				}
				s.pending.Add(-n)
				s.frames.Add(-1)
				select {
				case s.wake <- struct{}{}:
				default:
				}
			}
		case bMuxEOF:
			if len(data) != 0 {
				return errors.New("invalid stream EOF")
			}
			if s := m.get(id); s != nil {
				if s.remoteEOF.Swap(true) {
					return errors.New("duplicate stream EOF")
				}
				select {
				case <-s.done:
				case s.queue <- nil:
				default:
					return errors.New("EOF exceeded window")
				}
			}
		case bMuxClose:
			if len(data) != 0 {
				return errors.New("invalid stream close")
			}
			if s := m.get(id); s != nil {
				s.stop(false)
			}
		default:
			return errors.New("unknown device frame")
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
	list := make([]*bMuxStream, 0, len(m.streams))
	for _, s := range m.streams {
		list = append(list, s)
	}
	m.mu.Unlock()
	m.cancel()
	m.ws.CloseNow()
	for _, s := range list {
		s.stop(false)
	}
}
func (s *bMuxStream) stop(notify bool) {
	s.once.Do(func() {
		close(s.done)
		s.conn.Close()
		s.peer.mu.Lock()
		delete(s.peer.streams, s.id)
		s.peer.mu.Unlock()
		if notify {
			_ = s.peer.send(bMuxClose, s.id, nil)
		}
	})
}
func (s *bMuxStream) finish() {
	if s.readEnded.Load() && s.writeEnded.Load() {
		s.stop(false)
	}
}
func (s *bMuxStream) read() {
	defer s.peer.workers.Done()
	buffer := make([]byte, 16384)
	for {
		for s.pending.Load() >= 1048576 || s.frames.Load() >= 64 {
			select {
			case <-s.done:
				return
			case <-s.peer.ctx.Done():
				return
			case <-s.wake:
			}
		}
		n, e := s.conn.Read(buffer)
		if n > 0 {
			s.pending.Add(int64(n))
			s.frames.Add(1)
			if err := s.peer.send(bMuxData, s.id, buffer[:n]); err != nil {
				s.stop(false)
				return
			}
		}
		if e != nil {
			if e == io.EOF {
				s.readEnded.Store(true)
				_ = s.peer.send(bMuxEOF, s.id, nil)
				s.finish()
			} else {
				s.stop(true)
			}
			return
		}
	}
}
func (s *bMuxStream) write() {
	defer s.peer.workers.Done()
	for {
		select {
		case <-s.done:
			return
		case <-s.peer.ctx.Done():
			return
		case data := <-s.queue:
			if data == nil {
				if half, ok := s.conn.(interface{ CloseWrite() error }); ok {
					_ = half.CloseWrite()
				} else {
					s.conn.Close()
				}
				s.writeEnded.Store(true)
				s.finish()
				return
			}
			if _, e := io.CopyN(s.conn, bytes.NewReader(data), int64(len(data))); e != nil {
				s.stop(true)
				return
			}
			ack := make([]byte, 4)
			binary.BigEndian.PutUint32(ack, uint32(len(data)))
			if e := s.peer.send(bMuxAck, s.id, ack); e != nil {
				s.stop(false)
				return
			}
		}
	}
}

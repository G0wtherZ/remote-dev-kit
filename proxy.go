package main

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
)

func proxy(args []string) error {
	if len(args) != 3 {
		return errors.New("proxy requires a SOCKS address, destination and port")
	}
	h, _, e := net.SplitHostPort(args[0])
	if e != nil {
		return e
	}
	if h != "localhost" && !net.ParseIP(h).IsLoopback() {
		return errors.New("only a loopback SOCKS endpoint is allowed")
	}
	p, e := strconv.Atoi(args[2])
	if e != nil || p < 1 || p > 65535 {
		return errors.New("invalid destination port")
	}
	c, e := net.Dial("tcp", args[0])
	if e != nil {
		return e
	}
	defer c.Close()
	if _, e = c.Write([]byte{5, 1, 0}); e != nil {
		return e
	}
	reply := make([]byte, 2)
	if _, e = io.ReadFull(c, reply); e != nil {
		return e
	}
	if reply[0] != 5 || reply[1] != 0 {
		return errors.New("SOCKS endpoint rejected the connection")
	}
	request := []byte{5, 1, 0}
	ip := net.ParseIP(args[1])
	if ip4 := ip.To4(); ip4 != nil {
		request = append(request, 1)
		request = append(request, ip4...)
	} else if ip != nil {
		request = append(request, 4)
		request = append(request, ip.To16()...)
	} else {
		if len(args[1]) > 255 {
			return errors.New("host name too long")
		}
		request = append(request, 3, byte(len(args[1])))
		request = append(request, []byte(args[1])...)
	}
	request = binary.BigEndian.AppendUint16(request, uint16(p))
	if _, e = c.Write(request); e != nil {
		return e
	}
	header := make([]byte, 4)
	if _, e = io.ReadFull(c, header); e != nil {
		return e
	}
	if header[0] != 5 || header[1] != 0 {
		return errors.New("private-network destination is unavailable")
	}
	n := 0
	switch header[3] {
	case 1:
		n = 4
	case 4:
		n = 16
	case 3:
		b := []byte{0}
		if _, e = io.ReadFull(c, b); e != nil {
			return e
		}
		n = int(b[0])
	default:
		return errors.New("invalid SOCKS response")
	}
	if _, e = io.CopyN(io.Discard, c, int64(n+2)); e != nil {
		return e
	}
	done := make(chan struct{})
	go func() {
		io.Copy(c, os.Stdin)
		if tcp, ok := c.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
		close(done)
	}()
	_, e = io.Copy(os.Stdout, c)
	return e
}

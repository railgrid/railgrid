/*
Copyright 2026 The Railgrid Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0
*/

package hijackio

import (
	"bufio"
	"io"
	"net"
	"sync/atomic"
	"testing"
)

type countingConn struct {
	net.Conn
	reads  atomic.Int64
	writes atomic.Int64
}

func (c *countingConn) Read(p []byte) (int, error) {
	c.reads.Add(1)
	return c.Conn.Read(p)
}

func (c *countingConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	return c.Conn.Write(p)
}

func TestRebindPreservesBufferedInputAndRoutesBuffersThroughDecorator(t *testing.T) {
	server, peer := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = peer.Close() }()
	peerRead := make(chan int, 1)
	go func() {
		_, _ = io.WriteString(peer, "early")
		buf := make([]byte, len("pendinglater"))
		n, _ := io.ReadFull(peer, buf)
		peerRead <- n
	}()
	reader := bufio.NewReaderSize(server, 1024)
	if _, err := reader.Peek(5); err != nil {
		t.Fatal(err)
	}
	original := bufio.NewReadWriter(reader, bufio.NewWriterSize(server, 1024))
	if _, err := original.WriteString("pending"); err != nil {
		t.Fatal(err)
	}
	var decorated *countingConn
	_, rebound, err := Rebind(server, original, func(conn net.Conn) net.Conn {
		decorated = &countingConn{Conn: conn}
		return decorated
	})
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if n, err := rebound.ReadByte(); err != nil || n != 'e' {
		t.Fatalf("buffered first byte = %q, %v", n, err)
	}
	if decorated.reads.Load() == 0 {
		t.Fatal("buffered reader bypassed decorated connection")
	}
	if _, err := rebound.WriteString("later"); err != nil {
		t.Fatal(err)
	}
	if err := rebound.Flush(); err != nil {
		t.Fatal(err)
	}
	if decorated.writes.Load() == 0 {
		t.Fatal("buffered writer bypassed decorated connection")
	}
	// The old writer was flushed before rebinding; its bytes reached the peer
	// before writes through the new guarded writer.
	if n := <-peerRead; n != len("pendinglater") {
		t.Fatalf("peer received %d bytes, want pending plus later bytes", n)
	}
}

func TestRebindFlushesPendingWriterBeforeReplacingBuffers(t *testing.T) {
	server, peer := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = peer.Close() }()
	original := bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriterSize(server, 1024))
	if _, err := original.WriteString("pending"); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, len("pending"))
		_, _ = io.ReadFull(peer, buf)
		got <- string(buf)
	}()
	_, rebound, err := Rebind(server, original, func(conn net.Conn) net.Conn { return conn })
	if err != nil {
		t.Fatal(err)
	}
	if value := <-got; value != "pending" {
		t.Fatalf("pending bytes=%q", value)
	}
	if rebound == original {
		t.Fatal("returned original unguarded buffers")
	}
}

// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package restapi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

var errOrgTrustReadRevoked = errors.New("organization trust revoked")

func TestOrgTrustGatedConnDiscardsBytesReleasedAfterRevocation(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var revoked atomic.Bool
	testNow := time.Now()
	checks := make(chan struct{}, 2)
	guard := &orgTrustRefreshGuard{
		nextCheck: testNow,
		now:       func() time.Time { return testNow },
		check: func() error {
			checks <- struct{}{}
			if revoked.Load() {
				return errOrgTrustReadRevoked
			}
			return nil
		},
	}
	connection := &orgTrustGatedConn{
		Conn:   serverConn,
		guard:  guard,
		cancel: cancel,
	}
	var forwarded bytes.Buffer
	copyDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(&forwarded, connection)
		copyDone <- err
	}()

	<-checks // initial authorization passed; the socket read is now blocked.
	guard.mu.Lock()
	revoked.Store(true)
	testNow = guard.nextCheck
	guard.mu.Unlock()
	writeDone := make(chan error, 1)
	go func() {
		_, err := clientConn.Write([]byte("revoked organization stream bytes"))
		writeDone <- err
	}()
	if err := <-copyDone; !errors.Is(err, errOrgTrustReadRevoked) {
		t.Fatalf("io.Copy error = %v, want revocation error", err)
	}
	if forwarded.Len() != 0 {
		t.Fatalf("io.Copy forwarded %q after authorization was revoked", forwarded.String())
	}
	if err := ctx.Err(); err == nil {
		t.Fatal("revoked organization stream did not cancel its context")
	}
	_ = clientConn.SetDeadline(time.Now().Add(time.Second))
	select {
	case <-writeDone:
	case <-time.After(time.Second):
		t.Fatal("peer write did not finish after connection close")
	}
}

func TestOrgTrustGatedConnClosesIdleConnectionWhenAuthorizationExpires(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var checks atomic.Int32
	guard := &orgTrustRefreshGuard{
		ctx:       ctx,
		nextCheck: time.Now().Add(20 * time.Millisecond),
		check: func() error {
			checks.Add(1)
			return errOrgTrustReadRevoked
		},
	}
	connection := &orgTrustGatedConn{Conn: serverConn, guard: guard, cancel: cancel, done: make(chan struct{})}
	connection.startMonitor()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("idle stream was not cancelled after authorization refresh failed")
	}
	if checks.Load() != 1 {
		t.Fatalf("authorization refreshes=%d, want 1", checks.Load())
	}
	if _, err := clientConn.Read(make([]byte, 1)); err == nil {
		t.Fatal("idle peer connection remained open after authorization failure")
	}
}

type orgTrustHijackResponseWriter struct {
	header http.Header
	conn   net.Conn
	rw     *bufio.ReadWriter
}

func (w *orgTrustHijackResponseWriter) Header() http.Header { return w.header }
func (*orgTrustHijackResponseWriter) WriteHeader(int)       {}
func (*orgTrustHijackResponseWriter) Write(p []byte) (int, error) {
	return len(p), nil
}
func (w *orgTrustHijackResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.rw != nil {
		return w.conn, w.rw, nil
	}
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

func TestOrgTrustHijackReadWriterPreservesBuffersAndUsesGuardedConn(t *testing.T) {
	serverConn, peerConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	defer func() { _ = peerConn.Close() }()
	peerRead := make(chan string, 1)
	go func() {
		_, _ = io.WriteString(peerConn, "prefetched")
		buf := make([]byte, len("pendingwriter"))
		n, _ := io.ReadFull(peerConn, buf)
		peerRead <- string(buf[:n])
	}()
	reader := bufio.NewReaderSize(serverConn, 4096)
	if _, err := reader.Peek(len("prefetched")); err != nil {
		t.Fatal(err)
	}
	rw := bufio.NewReadWriter(reader, bufio.NewWriterSize(serverConn, 1024))
	if _, err := rw.WriteString("pending"); err != nil {
		t.Fatal(err)
	}
	var clock atomic.Int64
	now := time.Now()
	clock.Store(now.UnixNano())
	var checks atomic.Int64
	ctx := context.Background()
	guard := &orgTrustRefreshGuard{
		ctx:       ctx,
		nextCheck: now.Add(orgTrustStreamCheckInterval),
		now:       func() time.Time { return time.Unix(0, clock.Load()) },
		check:     func() error { checks.Add(1); return nil },
	}
	writer := &orgTrustGatedWriter{
		target: &orgTrustHijackResponseWriter{header: make(http.Header), conn: serverConn, rw: rw},
		guard:  guard,
		header: make(http.Header),
	}
	conn, rebound, err := writer.Hijack()
	if err != nil {
		t.Fatalf("Hijack: %v", err)
	}
	clock.Store(now.Add(2 * orgTrustStreamCheckInterval).UnixNano())
	var got [10]byte
	if _, err := io.ReadFull(rebound.Reader, got[:]); err != nil || string(got[:]) != "prefetched" {
		t.Fatalf("read prebuffer=%q err=%v", got, err)
	}
	if checks.Load() == 0 {
		t.Fatal("returned reader bypassed trust guard")
	}
	clock.Store(now.Add(4 * orgTrustStreamCheckInterval).UnixNano())
	if _, err := rebound.WriteString("writer"); err != nil {
		t.Fatal(err)
	}
	if err := rebound.Flush(); err != nil {
		t.Fatal(err)
	}
	if checks.Load() < 2 {
		t.Fatalf("returned writer guard checks=%d, want read and write refreshes", checks.Load())
	}
	if got := <-peerRead; got != "pendingwriter" {
		t.Fatalf("peer bytes=%q", got)
	}
	_ = conn.Close()
}

func TestOrgTrustHijackedConnAuthorizationContextSurvivesHandlerReturn(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	dispatchCtx, cancelDispatch := context.WithCancel(requestCtx)
	defer cancelDispatch()

	var guard *orgTrustRefreshGuard
	guard = &orgTrustRefreshGuard{
		ctx: dispatchCtx,
		check: func() error {
			if guard.ctx.Err() != nil {
				return guard.ctx.Err()
			}
			return nil
		},
		nextCheck: time.Now(),
	}
	writer := &orgTrustGatedWriter{
		target: &orgTrustHijackResponseWriter{header: make(http.Header), conn: serverConn},
		guard:  guard,
		cancel: cancelDispatch,
		header: make(http.Header),
	}
	conn, _, err := writer.Hijack()
	if err != nil {
		t.Fatalf("Hijack() error = %v", err)
	}
	// DispatchOrgTrust's deferred cancellation happens as soon as its dispatcher
	// returns after Hijack. The stream guard must already use the detached context.
	cancelDispatch()
	if err := guard.checkDue(); err != nil {
		t.Fatalf("authorization check after handler return = %v", err)
	}
	if err := guard.ctx.Err(); err != nil {
		t.Fatalf("stream authorization context was canceled with handler: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close hijacked connection: %v", err)
	}
	if err := guard.ctx.Err(); err == nil {
		t.Fatal("closing hijacked connection did not cancel stream authorization context")
	}
}

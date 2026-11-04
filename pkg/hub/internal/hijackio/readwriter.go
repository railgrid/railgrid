/*
Copyright 2026 The Railgrid Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0
*/

// Package hijackio rebinds Hijacker buffers to a decorated connection while
// preserving bytes already read by net/http before the hijack.
package hijackio

import (
	"bufio"
	"errors"
	"net"
)

// Rebind flushes any bytes buffered for writing, then returns a new buffered
// reader/writer whose network operations use the decorated connection. The
// original reader is retained only as a source of bytes already buffered from
// the peer; subsequent reads go through the decorated connection. decorate is
// normally a request authorization/activity wrapper.
func Rebind(conn net.Conn, rw *bufio.ReadWriter, decorate func(net.Conn) net.Conn) (net.Conn, *bufio.ReadWriter, error) {
	if conn == nil || rw == nil || rw.Reader == nil || rw.Writer == nil || decorate == nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, nil, errors.New("hijack I/O rebind requires a connection, buffers and decorator")
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	buffered := &bufferedReaderConn{Conn: conn, reader: rw.Reader}
	wrapped := decorate(buffered)
	if wrapped == nil {
		_ = conn.Close()
		return nil, nil, errors.New("hijack I/O decorator returned a nil connection")
	}
	reader := bufio.NewReaderSize(wrapped, rw.Reader.Size())
	writer := bufio.NewWriterSize(wrapped, rw.Writer.Size())
	return wrapped, bufio.NewReadWriter(reader, writer), nil
}

type bufferedReaderConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedReaderConn) Read(p []byte) (int, error) {
	if c.reader.Buffered() > 0 {
		return c.reader.Read(p)
	}
	return c.Conn.Read(p)
}

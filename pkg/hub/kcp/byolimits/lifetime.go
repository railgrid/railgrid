// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package byolimits

import (
	"bufio"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apiserver/pkg/endpoints/responsewriter"
)

// lifetime keeps a slot until both the handler and an upgraded connection have
// finished. A handler returning after Hijack must not make its connection free.
type lifetime struct {
	http.ResponseWriter
	deadline   time.Time
	release    func()
	mu         sync.Mutex
	finished   bool
	connection *limitedConn
}

func newLifetime(w http.ResponseWriter, deadline time.Time, release func()) *lifetime {
	return &lifetime{ResponseWriter: w, deadline: deadline, release: release}
}

func (l *lifetime) Unwrap() http.ResponseWriter { return l.ResponseWriter }
func (l *lifetime) writer() http.ResponseWriter { return responsewriter.WrapForHTTP1Or2(l) }

func (l *lifetime) prepare() error {
	// kcp's timeout/audit wrappers may not expose ResponseController directly;
	// its supported unwrapping helper reaches net/http's original writer.
	c := http.NewResponseController(responsewriter.GetOriginal(l.ResponseWriter))
	if err := c.SetReadDeadline(l.deadline); err != nil {
		return err
	}
	return c.SetWriteDeadline(l.deadline)
}

func (l *lifetime) finish() {
	l.mu.Lock()
	l.finished = true
	conn := l.connection
	l.mu.Unlock()
	if conn == nil {
		c := http.NewResponseController(responsewriter.GetOriginal(l.ResponseWriter))
		_ = c.SetReadDeadline(time.Time{})
		_ = c.SetWriteDeadline(time.Time{})
		l.release()
	} else if conn.closed.Load() {
		l.release()
	}
}

func (l *lifetime) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(l.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	if err := conn.SetDeadline(l.deadline); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	wrapper := &limitedConn{Conn: conn, owner: l}
	l.mu.Lock()
	l.connection = wrapper
	// Deadline bounds reads/writes; Close also bounds an idle connection that
	// nobody reads or writes after handing control to an asynchronous consumer.
	wrapper.timer = time.AfterFunc(time.Until(l.deadline), func() { _ = wrapper.Close() })
	l.mu.Unlock()
	return wrapper, rw, nil
}

type limitedConn struct {
	net.Conn
	owner  *lifetime
	once   sync.Once
	closed atomic.Bool
	timer  *time.Timer
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.closed.Store(true)
		c.owner.mu.Lock()
		if c.timer != nil {
			c.timer.Stop()
		}
		finished := c.owner.finished
		c.owner.mu.Unlock()
		if finished {
			c.owner.release()
		}
	})
	return err
}

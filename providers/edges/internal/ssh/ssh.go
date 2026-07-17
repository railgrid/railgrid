/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package ssh provides SSH session utilities over WebSocket connections.
package ssh

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sync/errgroup"
	"k8s.io/klog/v2"
)

// errSessionDone is a sentinel returned by wait() when the SSH session exits
// cleanly (nil error from session.Wait).  It causes errgroup to cancel gCtx
// so that receiveWsMsg, sendComboOutput, sendKeepAlive, and wsCloser all
// return promptly.  Run() filters this error out and returns nil to callers.
var errSessionDone = errors.New("ssh session ended normally")

type SocketSSHSession struct {
	stdinPipe   io.WriteCloser
	comboOutput *safeBuffer     // ssh output
	session     *ssh.Session    // pseudo terminal session
	wsConn      *websocket.Conn // client conn
	logger      klog.Logger
}

func NewSocketSSHSession(l klog.Logger, cols, rows int, sshClient *ssh.Client, wsConn *websocket.Conn) (*SocketSSHSession, error) {
	sshSession, err := sshClient.NewSession()
	if err != nil {
		return nil, err
	}

	stdinP, err := sshSession.StdinPipe()
	if err != nil {
		return nil, err
	}

	comboWriter := new(safeBuffer)

	// ssh stdout and stderr will write output into comboWriter
	sshSession.Stdout = comboWriter
	sshSession.Stderr = comboWriter

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,     // enable echo (1 = on, 0 = off)
		ssh.TTY_OP_ISPEED: 14400, // input speed = 14.4kbaud
		ssh.TTY_OP_OSPEED: 14400, // output speed = 14.4kbaud
	}
	// Request pseudo terminal
	if err := sshSession.RequestPty("xterm", rows, cols, modes); err != nil {
		return nil, err
	}
	// Start remote shell
	if err := sshSession.Shell(); err != nil {
		return nil, err
	}
	return &SocketSSHSession{
		stdinPipe:   stdinP,
		comboOutput: comboWriter,
		session:     sshSession,
		wsConn:      wsConn,
		logger:      l.WithName("ws_ssh_session"),
	}, nil
}

func (s *SocketSSHSession) Close() {
	// Close stdinPipe first to signal EOF to the remote shell and to unblock
	// any goroutine waiting on the pipe (e.g. session.Wait's stdin copier).
	if s.stdinPipe != nil {
		s.stdinPipe.Close() //nolint:errcheck
	}
	if s.session != nil {
		s.session.Close() //nolint:errcheck
	}
	if s.comboOutput != nil {
		s.comboOutput = nil
	}
}

func (s *SocketSSHSession) Run(ctx context.Context) error {
	g, gCtx := errgroup.WithContext(ctx)

	// flushDone is closed when sendComboOutput returns, signalling that the
	// last flush has been written to the WebSocket.  The wsCloser goroutine
	// waits for this before closing the WebSocket so the CLI always receives
	// all output before it sees EOF.
	flushDone := make(chan struct{})

	g.Go(func() error {
		return s.receiveWsMsg(gCtx.Done())
	})

	g.Go(func() error {
		defer close(flushDone)
		return s.sendComboOutput(gCtx.Done())
	})

	g.Go(func() error {
		return s.sendKeepAlive(gCtx.Done())
	})

	g.Go(func() error {
		return s.wait(gCtx.Done())
	})

	// wsCloser: wait until sendComboOutput has finished its final flush, then
	// close the WebSocket so that receiveWsMsg (blocked on ReadMessage) unblocks
	// and the errgroup can finish.
	//
	// We ONLY wait on flushDone (not gCtx.Done()) to guarantee the flush
	// happens before the close.  flushDone is always closed via defer in the
	// sendComboOutput goroutine, so wsCloser will never hang: even if
	// sendComboOutput hits a write error it still returns and defers the close.
	g.Go(func() error {
		<-flushDone
		s.wsConn.Close() //nolint:errcheck
		return nil
	})

	err := g.Wait()
	if errors.Is(err, errSessionDone) {
		return nil
	}
	return err
}

func (s *SocketSSHSession) receiveWsMsg(stop <-chan struct{}) error {
	wsConn := s.wsConn

	for {
		select {
		case <-stop:
			return nil
		default:
			_, wsData, err := wsConn.ReadMessage()
			if err != nil {
				s.logger.Error(err, "reading webSocket message failed")
				return err
			}

			var msg wsMsg
			if err := json.Unmarshal(wsData, &msg); err != nil {
				s.logger.Error(err, "failed to unmarshal websocket message",
					"data", string(wsData),
				)
				continue
			}
			switch msg.Type {
			case wsMsgResize:
				if msg.Cols > 0 && msg.Rows > 0 {
					if err := s.session.WindowChange(msg.Rows, msg.Cols); err != nil {
						s.logger.Error(err, "failed to change ssh pty size")
					}
				}
			case wsMsgCmd:
				decodeBytes, err := base64.StdEncoding.DecodeString(msg.Cmd)
				if err != nil {
					s.logger.Error(err, "failed to decode ws cmd base64 msg")
				}
				s.writeToSSHPipe(decodeBytes)
			case wsMsgHeartbeat:
				// heartbeat to keep WebSocket connection alive
			}
		}
	}
}

func (s *SocketSSHSession) writeToSSHPipe(cmdBytes []byte) {
	if _, err := s.stdinPipe.Write(cmdBytes); err != nil {
		s.logger.Error(err, "failed to write to ssh stdin pipe")
	}
}

func (s *SocketSSHSession) flushOutput() error {
	if s.comboOutput == nil {
		return nil
	}
	// ReadAndReset copies the current buffer content and resets it atomically
	// under the safeBuffer mutex.  This prevents a race where a concurrent
	// Write() (from the SSH library's copy goroutine) appends new data between
	// the Bytes() read and the Reset() call, causing that data to be silently
	// discarded and never forwarded to the WebSocket client.
	bs := s.comboOutput.ReadAndReset()
	if len(bs) == 0 {
		return nil
	}
	if err := s.wsConn.WriteMessage(websocket.BinaryMessage, bs); err != nil {
		s.logger.Error(err, "failed to write ssh output to the websocket conn")
		return err
	}
	return nil
}

func (s *SocketSSHSession) sendComboOutput(stop <-chan struct{}) error {
	tick := time.NewTicker(time.Millisecond * time.Duration(60))
	defer tick.Stop()
	for {
		select {
		case <-stop:
			// Flush remaining output before exiting — otherwise output produced
			// between the last tick and session-end (e.g. shell exit) is lost.
			return s.flushOutput()
		case <-tick.C:
			if err := s.flushOutput(); err != nil {
				return err
			}
		}
	}
}

func (s *SocketSSHSession) sendKeepAlive(stop <-chan struct{}) error {
	tick := time.NewTicker(time.Second * 15)
	defer tick.Stop()

	consecutiveFailures := 0
	const maxConsecutiveFailures = 3

	for {
		select {
		case <-stop:
			return nil
		case <-tick.C:
			// Use wantReply=false so SendRequest never blocks if the channel
			// is broken or the server has already closed the session.
			_, err := s.session.SendRequest("keepalive@openssh.com", false, nil)
			if err != nil {
				consecutiveFailures++

				if consecutiveFailures <= maxConsecutiveFailures {
					s.logger.Error(err, "failed to send SSH keep-alive", "consecutiveFailures", consecutiveFailures)
				}

				if consecutiveFailures >= maxConsecutiveFailures {
					s.logger.Info("SSH session appears dead after multiple keepalive failures, stopping keepalive loop")
					return err
				}
			} else if consecutiveFailures > 0 {
				s.logger.Info("SSH keepalive recovered", "previousFailures", consecutiveFailures)
				consecutiveFailures = 0
			}
		}
	}
}

func (s *SocketSSHSession) wait(stop <-chan struct{}) error {
	done := make(chan error, 1)

	go func() {
		err := s.session.Wait()
		if err != nil {
			s.logger.Error(err, "ssh session error")
		}
		done <- err
	}()

	select {
	case <-stop:
		// Close stdinPipe to unblock the SSH library's stdin copy goroutine,
		// then close the session to unblock session.Wait().
		if s.stdinPipe != nil {
			s.stdinPipe.Close() //nolint:errcheck
		}
		s.session.Close() //nolint:errcheck
		return nil
	case err := <-done:
		if err == nil {
			// Return a non-nil sentinel so errgroup cancels gCtx, which
			// causes receiveWsMsg, sendComboOutput, sendKeepAlive, and
			// wsCloser to all return and unblock g.Wait().  Run() filters
			// this sentinel out before returning to callers.
			return errSessionDone
		}
		return err
	}
}

type safeBuffer struct {
	buffer bytes.Buffer
	mu     sync.Mutex
}

func (w *safeBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(p)
}
func (w *safeBuffer) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Bytes()
}
func (w *safeBuffer) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buffer.Reset()
}

// ReadAndReset returns a copy of the current buffer content and resets the
// buffer atomically under the mutex.  Using a single locked operation instead
// of separate Bytes()+Reset() calls eliminates the race where a concurrent
// Write() can append data between the read and the reset, causing that data
// to be silently discarded.
func (w *safeBuffer) ReadAndReset() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buffer.Len() == 0 {
		return nil
	}
	data := make([]byte, w.buffer.Len())
	copy(data, w.buffer.Bytes())
	w.buffer.Reset()
	return data
}

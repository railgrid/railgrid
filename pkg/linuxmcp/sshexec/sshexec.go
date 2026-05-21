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

// Package sshexec is the shared "run one shell command over SSH" helper used
// by every LinuxMCP toolset.  It centralises timeout / output-cap / exit-code
// handling so tool implementations stay focused on argument shaping.
package sshexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/faroshq/faros-kedge/pkg/linuxmcp"
)

// Result is the structured outcome of running a shell command on an edge.
type Result struct {
	// Target is the edge the command ran on (defaulted from the Provider if
	// the caller passed an empty target).
	Target string `json:"target"`
	// ExitCode is the remote exit status.  Set even when err is nil and the
	// command exited non-zero — the caller decides what that means.
	ExitCode int `json:"exitCode"`
	// Stdout / Stderr are capped at Provider.MaxOutputBytes().
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	// Truncated reports whether either stream hit the cap.
	Truncated bool `json:"truncated,omitempty"`
	// DurationMs is the wall-clock duration of the remote run.
	DurationMs int64 `json:"durationMs"`
}

// Run executes a single shell command on the resolved target edge.
//
// A nil error means the command ran to completion (possibly with ExitCode!=0).
// A non-nil error means the command could not even be started (session
// failed, dial failed) or the wall-clock timeout fired.
func Run(ctx context.Context, p *linuxmcp.Provider, target, cmd string) (Result, error) {
	if target == "" {
		target = p.DefaultTarget()
	}

	ctx, cancel := context.WithTimeout(ctx, p.CommandTimeout())
	defer cancel()

	client, err := p.OpenSession(ctx, target)
	if err != nil {
		return Result{Target: target}, fmt.Errorf("open session: %w", err)
	}
	defer client.Close() //nolint:errcheck

	session, err := client.NewSession()
	if err != nil {
		return Result{Target: target}, fmt.Errorf("new ssh session: %w", err)
	}
	defer session.Close() //nolint:errcheck

	maxOut := p.MaxOutputBytes()
	var stdoutBuf, stderrBuf bytes.Buffer
	session.Stdout = &limitedWriter{w: &stdoutBuf, n: int64(maxOut)}
	session.Stderr = &limitedWriter{w: &stderrBuf, n: int64(maxOut)}

	start := time.Now()
	runErr := session.Run(cmd)
	elapsed := time.Since(start)

	res := Result{
		Target:     target,
		Stdout:     stdoutBuf.String(),
		Stderr:     stderrBuf.String(),
		Truncated:  stdoutBuf.Len() >= maxOut || stderrBuf.Len() >= maxOut,
		DurationMs: elapsed.Milliseconds(),
	}

	if runErr == nil {
		return res, nil
	}
	var ee *gossh.ExitError
	if errors.As(runErr, &ee) {
		res.ExitCode = ee.ExitStatus()
		return res, nil
	}
	if ctx.Err() != nil {
		return res, fmt.Errorf("timed out after %s", p.CommandTimeout())
	}
	return res, runErr
}

// ShellQuote single-quotes a string for safe POSIX shell interpolation.
//
// Embedded single quotes are escaped using the close-quote / escaped-quote /
// reopen-quote trick: foo'bar → 'foo'\”bar'.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// limitedWriter writes at most n bytes to the underlying writer, dropping the
// rest.  Prevents runaway commands from blowing the hub's heap.
type limitedWriter struct {
	w io.Writer
	n int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	if int64(len(p)) <= l.n {
		n, err := l.w.Write(p)
		l.n -= int64(n)
		return n, err
	}
	n, err := l.w.Write(p[:l.n])
	l.n -= int64(n)
	if err != nil {
		return n, err
	}
	return len(p), nil
}

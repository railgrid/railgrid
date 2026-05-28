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

package core

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp"
	"github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp/sshexec"
)

// shellQuote is a local alias for sshexec.ShellQuote.
func shellQuote(s string) string { return sshexec.ShellQuote(s) }

// ─── read_file ───────────────────────────────────────────────────────────────

type ReadFileInput struct {
	Path   string `json:"path" jsonschema:"absolute path on the edge filesystem"`
	Target string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

type ReadFileOutput struct {
	Target     string `json:"target"`
	Path       string `json:"path"`
	ContentB64 string `json:"contentB64"`
	SizeBytes  int    `json:"sizeBytes"`
	Truncated  bool   `json:"truncated,omitempty"`
}

func readFileHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[ReadFileInput, ReadFileOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ReadFileInput) (*mcp.CallToolResult, ReadFileOutput, error) {
		if in.Path == "" {
			return nil, ReadFileOutput{}, fmt.Errorf("read_file: \"path\" is required")
		}
		// `cat -- file | base64 -w0` is portable across coreutils/busybox; -w0
		// disables line wrapping so the entire payload is a single token.
		cmd := fmt.Sprintf("cat -- %s | base64 -w0", shellQuote(in.Path))
		res, err := execShell(ctx, p, in.Target, cmd)
		if err != nil {
			return nil, ReadFileOutput{Target: in.Target, Path: in.Path}, fmt.Errorf("read_file: %w", err)
		}
		if res.ExitCode != 0 {
			return nil, ReadFileOutput{Target: res.Target, Path: in.Path},
				fmt.Errorf("read_file: exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
		}
		// res.Stdout is the base64 payload; decode just to compute size.
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(res.Stdout))
		if err != nil {
			return nil, ReadFileOutput{Target: res.Target, Path: in.Path},
				fmt.Errorf("read_file: decoding remote base64 output: %w", err)
		}
		return nil, ReadFileOutput{
			Target:     res.Target,
			Path:       in.Path,
			ContentB64: strings.TrimSpace(res.Stdout),
			SizeBytes:  len(decoded),
			Truncated:  res.Truncated,
		}, nil
	}
}

// ─── write_file ──────────────────────────────────────────────────────────────

type WriteFileInput struct {
	Path       string `json:"path" jsonschema:"absolute path on the edge filesystem"`
	ContentB64 string `json:"contentB64" jsonschema:"base64-encoded file content"`
	Mode       string `json:"mode,omitempty" jsonschema:"octal file mode (e.g. \"0644\"); applied with chmod after write if set"`
	Target     string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

type WriteFileOutput struct {
	Target       string `json:"target"`
	Path         string `json:"path"`
	BytesWritten int    `json:"bytesWritten"`
}

func writeFileHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[WriteFileInput, WriteFileOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in WriteFileInput) (*mcp.CallToolResult, WriteFileOutput, error) {
		if in.Path == "" {
			return nil, WriteFileOutput{}, fmt.Errorf("write_file: \"path\" is required")
		}
		if in.ContentB64 == "" {
			return nil, WriteFileOutput{}, fmt.Errorf("write_file: \"contentB64\" is required")
		}
		decoded, err := base64.StdEncoding.DecodeString(in.ContentB64)
		if err != nil {
			return nil, WriteFileOutput{}, fmt.Errorf("write_file: invalid contentB64: %w", err)
		}
		if in.Mode != "" {
			if _, err := strconv.ParseInt(in.Mode, 8, 32); err != nil {
				return nil, WriteFileOutput{}, fmt.Errorf("write_file: invalid mode %q (expected octal like 0644): %w", in.Mode, err)
			}
		}

		// Pipe the base64 payload as stdin to `base64 -d > file`.  We use a
		// here-string-ish printf so the payload doesn't need to fit in argv
		// limits, and an explicit umask + chmod for predictable permissions.
		// The full command is single-line to play nicely with session.Run.
		cmd := fmt.Sprintf("set -e; printf %%s %s | base64 -d > %s",
			shellQuote(in.ContentB64), shellQuote(in.Path))
		if in.Mode != "" {
			cmd += "; chmod " + in.Mode + " " + shellQuote(in.Path)
		}
		res, err := execShell(ctx, p, in.Target, cmd)
		if err != nil {
			return nil, WriteFileOutput{Target: in.Target, Path: in.Path}, fmt.Errorf("write_file: %w", err)
		}
		if res.ExitCode != 0 {
			return nil, WriteFileOutput{Target: res.Target, Path: in.Path},
				fmt.Errorf("write_file: exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
		}
		return nil, WriteFileOutput{
			Target:       res.Target,
			Path:         in.Path,
			BytesWritten: len(decoded),
		}, nil
	}
}

// ─── list_dir ────────────────────────────────────────────────────────────────

type ListDirInput struct {
	Path   string `json:"path" jsonschema:"absolute directory path on the edge filesystem"`
	Target string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

type ListDirOutput struct {
	Target string `json:"target"`
	Path   string `json:"path"`
	// Raw `ls -la --time-style=long-iso` output.  Structured parsing is
	// intentionally left to the caller to avoid disagreements between
	// coreutils and busybox ls dialects.
	Listing   string `json:"listing"`
	Truncated bool   `json:"truncated,omitempty"`
}

func listDirHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[ListDirInput, ListDirOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListDirInput) (*mcp.CallToolResult, ListDirOutput, error) {
		if in.Path == "" {
			return nil, ListDirOutput{}, fmt.Errorf("list_dir: \"path\" is required")
		}
		// --time-style=long-iso is GNU-only; fall back to plain ls on failure.
		cmd := fmt.Sprintf("ls -la --time-style=long-iso -- %s 2>/dev/null || ls -la -- %s",
			shellQuote(in.Path), shellQuote(in.Path))
		res, err := execShell(ctx, p, in.Target, cmd)
		if err != nil {
			return nil, ListDirOutput{Target: in.Target, Path: in.Path}, fmt.Errorf("list_dir: %w", err)
		}
		if res.ExitCode != 0 {
			return nil, ListDirOutput{Target: res.Target, Path: in.Path},
				fmt.Errorf("list_dir: exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
		}
		return nil, ListDirOutput{
			Target:    res.Target,
			Path:      in.Path,
			Listing:   res.Stdout,
			Truncated: res.Truncated,
		}, nil
	}
}

// ─── stat_path ───────────────────────────────────────────────────────────────

type StatPathInput struct {
	Path   string `json:"path" jsonschema:"absolute path on the edge filesystem"`
	Target string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

type StatPathOutput struct {
	Target string `json:"target"`
	Path   string `json:"path"`
	// Exists reflects whether `stat` could resolve the path.
	Exists bool `json:"exists"`
	// Raw `stat -c '%F|%s|%a|%U|%G|%Y'` payload, split into fields.
	Type       string `json:"type,omitempty"`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
	ModeOctal  string `json:"modeOctal,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Group      string `json:"group,omitempty"`
	MTimeEpoch int64  `json:"mtimeEpoch,omitempty"`
}

func statPathHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[StatPathInput, StatPathOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in StatPathInput) (*mcp.CallToolResult, StatPathOutput, error) {
		if in.Path == "" {
			return nil, StatPathOutput{}, fmt.Errorf("stat_path: \"path\" is required")
		}
		cmd := fmt.Sprintf("stat -c '%%F|%%s|%%a|%%U|%%G|%%Y' -- %s", shellQuote(in.Path))
		res, err := execShell(ctx, p, in.Target, cmd)
		if err != nil {
			return nil, StatPathOutput{Target: in.Target, Path: in.Path}, fmt.Errorf("stat_path: %w", err)
		}
		out := StatPathOutput{Target: res.Target, Path: in.Path}
		if res.ExitCode != 0 {
			// `stat` returns non-zero on missing path; report as Exists=false
			// rather than a tool-level error so the caller can branch on it.
			return nil, out, nil
		}
		fields := strings.Split(strings.TrimSpace(res.Stdout), "|")
		if len(fields) != 6 {
			return nil, out, fmt.Errorf("stat_path: unexpected stat output: %q", res.Stdout)
		}
		out.Exists = true
		out.Type = fields[0]
		if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
			out.SizeBytes = v
		}
		out.ModeOctal = fields[2]
		out.Owner = fields[3]
		out.Group = fields[4]
		if v, err := strconv.ParseInt(fields[5], 10, 64); err == nil {
			out.MTimeEpoch = v
		}
		return nil, out, nil
	}
}

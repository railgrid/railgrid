/*
Copyright 2026 The Railgrid Authors.

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

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// newMCPCallCommand is the one-shot form of 'railgrid mcp proxy': it sends a
// single tools/call (or tools/list) through the same forwarder, as the
// logged-in user, and prints the tool's result. Shell scripts and coding
// agents without an MCP client get the provider tools without a JSON-RPC
// helper of their own.
func newMCPCallCommand() *cobra.Command {
	o := &mcpProxyOptions{}
	var list bool
	var argsFile string
	cmd := &cobra.Command{
		Use:   "call <tool> [json-arguments]",
		Short: "Call one provider tool on the workspace MCP endpoint, as you",
		Long: `Invoke a provider tool through the workspace's aggregate MCP endpoint with
your own railgrid login (the same path as 'railgrid mcp proxy'), and print
its result: the structured content when the tool returns one, otherwise the
text. A failing tool exits non-zero with the tool's message.

  railgrid mcp call --list                                         # every tool you can call
  railgrid mcp call infrastructure__list_instances
  railgrid mcp call code__build_status '{"repositoryRef":"shop"}'
  railgrid mcp call infrastructure__dev_sync --args-file sync.json

Arguments are a JSON object, inline or from --args-file ('-' reads stdin).`,
		Args: cobra.RangeArgs(0, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			if list {
				if len(args) > 0 {
					return fmt.Errorf("--list takes no tool name")
				}
				return runMCPList(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), o)
			}
			if len(args) == 0 {
				return fmt.Errorf("usage: railgrid mcp call <tool> [json-arguments] (or --list)")
			}
			toolArgs := json.RawMessage("{}")
			switch {
			case argsFile != "" && len(args) > 1:
				return fmt.Errorf("pass the arguments inline or with --args-file, not both")
			case argsFile == "-":
				b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 64<<20))
				if err != nil {
					return fmt.Errorf("reading arguments from stdin: %w", err)
				}
				toolArgs = b
			case argsFile != "":
				b, err := os.ReadFile(argsFile)
				if err != nil {
					return fmt.Errorf("reading %s: %w", argsFile, err)
				}
				toolArgs = b
			case len(args) > 1:
				toolArgs = json.RawMessage(args[1])
			}
			if !json.Valid(toolArgs) {
				return fmt.Errorf("arguments must be a JSON object")
			}
			return runMCPCall(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), o, args[0], toolArgs)
		},
	}
	o.target.addFlags(cmd)
	cmd.Flags().StringVar(&o.mcpserverName, "mcpserver-name", defaultMCPServerName, "Aggregate MCP server to call")
	cmd.Flags().BoolVar(&list, "list", false, "List the tools the endpoint federates for you instead of calling one")
	cmd.Flags().StringVar(&argsFile, "args-file", "", "Read the JSON arguments from this file ('-' for stdin)")
	return cmd
}

// mcpOneShot forwards one JSON-RPC request through the proxy and returns the
// reply line(s) it wrote.
func mcpOneShot(ctx context.Context, errOut io.Writer, o *mcpProxyOptions, request any) ([]byte, error) {
	line, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	p := newMCPProxy(o.resolveTarget, &out, errOut)
	if err := p.run(ctx, bytes.NewReader(append(line, '\n'))); err != nil {
		return nil, err
	}
	if out.Len() == 0 {
		return nil, fmt.Errorf("the hub sent no reply")
	}
	return out.Bytes(), nil
}

func runMCPCall(ctx context.Context, out, errOut io.Writer, o *mcpProxyOptions, tool string, args json.RawMessage) error {
	body, err := mcpOneShot(ctx, errOut, o, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	if err != nil {
		return err
	}
	result, err := parseMCPToolResult(body)
	if err != nil {
		return err
	}
	var text string
	if json.Unmarshal(result, &text) == nil {
		_, err = fmt.Fprintln(out, text)
		return err
	}
	return printJSON(out, result)
}

func runMCPList(ctx context.Context, out, errOut io.Writer, o *mcpProxyOptions) error {
	body, err := mcpOneShot(ctx, errOut, o, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if err != nil {
		return err
	}
	var reply struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(body), &reply); err != nil {
		return fmt.Errorf("decoding tools/list: %w", err)
	}
	if reply.Error != nil {
		return fmt.Errorf("MCP error %d: %s", reply.Error.Code, reply.Error.Message)
	}
	tw := newTabWriter(out)
	for _, t := range reply.Result.Tools {
		printRow(tw, t.Name, oneLine(strings.TrimSpace(t.Description), 100))
	}
	return tw.Flush()
}

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

package mcpcodetest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/railgrid/provider-sdk/mcpcode"
)

// Unwrap adapts a fake that answers tools/call by tool name to the
// aggregate's wire. An execute request carrying the program mcpcode.CallCode
// writes is rewritten into the named tools/call it makes before it reaches
// next, and next's result is wrapped into the execute envelope on the way
// back; a JSON-RPC error from next passes through as is. Every other request
// reaches next untouched. It lets an existing by-name fake keep its
// assertions while the client under test speaks code mode.
func Unwrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					Code string `json:"code"`
				} `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		if req.Method != "tools/call" || req.Params.Name != mcpcode.ToolExecute {
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
			return
		}
		writeJSON := func(msg map[string]any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(msg)
		}
		tool, args, ok := mcpcode.ParseCallCode(req.Params.Arguments.Code)
		if !ok {
			writeJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": mcpcode.EncodeRunError("mcpcodetest.Unwrap runs only programs written by mcpcode.CallCode")})
			return
		}
		inner, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		forwarded := r.Clone(r.Context())
		forwarded.Body = io.NopCloser(bytes.NewReader(inner))
		forwarded.ContentLength = int64(len(inner))
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, forwarded)
		if rec.Code != 0 && (rec.Code < 200 || rec.Code > 299) {
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		payload := rec.Body.Bytes()
		if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
			for _, line := range strings.Split(string(payload), "\n") {
				if strings.HasPrefix(line, "data: ") {
					payload = []byte(strings.TrimPrefix(line, "data: "))
					break
				}
			}
		}
		var env struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(payload, &env); err != nil {
			writeJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": mcpcode.EncodeRunError(mcpcode.RefusalMessage(tool, "fake answered with invalid JSON: "+err.Error()))})
			return
		}
		if len(env.Error) > 0 && string(env.Error) != "null" {
			writeJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": env.Error})
			return
		}
		result := env.Result
		if len(bytes.TrimSpace(result)) == 0 || string(result) == "null" {
			result = json.RawMessage(`{"content":[]}`)
		}
		wrapped, err := mcpcode.EncodeCallResult(tool, result)
		if err != nil {
			writeJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": mcpcode.EncodeRunError(mcpcode.RefusalMessage(tool, err.Error()))})
			return
		}
		writeJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": wrapped})
	})
}

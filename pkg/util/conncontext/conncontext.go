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

// Package conncontext stores and retrieves net.Conn from context.
package conncontext

import (
	"context"
	"net"
	"net/http"
)

type key string

var ConnKey = key("http-conn")

func SaveConn(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, ConnKey, c)
}

func GetConn(r *http.Request) net.Conn {
	return r.Context().Value(ConnKey).(net.Conn)
}

/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package serve

import (
	"net"
	"testing"
)

func TestListenAddress(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{name: "default wildcard", want: ":8081"},
		{name: "host override", host: "127.0.0.1", want: "127.0.0.1:8081"},
		{name: "ipv6 host override", host: "::1", want: "[::1]:8081"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("RAILGRID_LISTEN_HOST", tt.host)
			if got := ListenAddress("8081"); got != tt.want {
				t.Fatalf("ListenAddress() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestListenAddressBindsLoopbackWhenConfigured(t *testing.T) {
	t.Setenv("RAILGRID_LISTEN_HOST", "127.0.0.1")
	listener, err := net.Listen("tcp", ListenAddress("0"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address type = %T, want *net.TCPAddr", listener.Addr())
	}
	if !addr.IP.IsLoopback() {
		t.Fatalf("listener IP = %s, want loopback", addr.IP)
	}
	if addr.Port == 0 {
		t.Fatal("listener did not receive an ephemeral port")
	}
}

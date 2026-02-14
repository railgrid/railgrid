package conncontext

import (
	"context"
	"net"
	"net/http"
	"testing"
)

func TestSaveAndGetConn(t *testing.T) {
	// Create a pair of connected pipes as our net.Conn
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	ctx := SaveConn(context.Background(), client)

	// Create a request with this context
	req, err := http.NewRequestWithContext(ctx, "GET", "http://localhost/", nil)
	if err != nil {
		t.Fatal(err)
	}

	got := GetConn(req)
	if got != client {
		t.Errorf("GetConn returned different connection: got %v, want %v", got, client)
	}
}

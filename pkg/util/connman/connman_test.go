package connman

import (
	"context"
	"testing"
)

func TestConnectionManager_DialNoConnection(t *testing.T) {
	cm := New()

	_, err := cm.Dial(context.Background(), "nonexistent")
	if err != ErrNoConnection {
		t.Errorf("expected ErrNoConnection, got %v", err)
	}
}

func TestConnectionManager_New(t *testing.T) {
	cm := New()
	if cm == nil {
		t.Fatal("expected non-nil ConnectionManager")
	}
	if cm.deviceDialers == nil {
		t.Fatal("expected initialized deviceDialers map")
	}
}

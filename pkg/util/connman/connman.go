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

package connman

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/faroshq/faros-kedge/pkg/util/revdial"
)

var (
	ErrNoConnection = errors.New("no connection")
)

// ConnectionManager manages connections to devices
type ConnectionManager struct {
	deviceDialers map[string]*revdial.Dialer
	lock          sync.RWMutex
}

func New() *ConnectionManager {
	return &ConnectionManager{
		deviceDialers: make(map[string]*revdial.Dialer),
	}
}

func (m *ConnectionManager) Set(key string, conn net.Conn) {
	m.lock.Lock()
	m.deviceDialers[key] = revdial.NewDialer(conn, "/tunnel/proxy")
	m.lock.Unlock()
}

func (m *ConnectionManager) Dial(ctx context.Context, key string) (net.Conn, error) {
	m.lock.RLock()
	dialer, ok := m.deviceDialers[key]
	if !ok {
		m.lock.RUnlock()
		return nil, ErrNoConnection
	}
	m.lock.RUnlock()

	return dialer.Dial(ctx)
}

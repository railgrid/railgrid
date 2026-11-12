// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package harness

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestSystemdConnectionRequiresOwnedPrivateRuntimeAndSocket(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := systemdEnvironmentAt(root, uint32(os.Geteuid())); err == nil {
		t.Fatal("missing bus accepted")
	}
	bus, err := net.Listen("unix", filepath.Join(root, "bus"))
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	env, err := systemdEnvironmentAt(root, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	if env["XDG_RUNTIME_DIR"] != root || env["DBUS_SESSION_BUS_ADDRESS"] != "unix:path="+filepath.Join(root, "bus") {
		t.Fatal("wrong local connection")
	}
	if _, err := systemdEnvironmentAt(root, uint32(os.Geteuid())+1); err == nil {
		t.Fatal("different owner accepted")
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := systemdEnvironmentAt(root, uint32(os.Geteuid())); err == nil {
		t.Fatal("public runtime accepted")
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "runtime")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := systemdEnvironmentAt(link, uint32(os.Geteuid())); err == nil {
		t.Fatal("symlink runtime accepted")
	}
}

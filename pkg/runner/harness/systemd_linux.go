// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// This is local operator opt-in, never task-provided connection data. Keep
// interactive home/XDG configuration and forge credentials isolated as before.
func userSystemdEnvironment() (map[string]string, error) {
	return systemdEnvironmentAt(filepath.Join("/run/user", strconv.Itoa(os.Geteuid())), uint32(os.Geteuid()))
}

func systemdEnvironmentAt(root string, uid uint32) (map[string]string, error) {
	dir, err := os.Lstat(root)
	if err != nil || !dir.IsDir() || dir.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("userSystemd requires a private current-user runtime directory")
	}
	owner, ok := dir.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uid {
		return nil, fmt.Errorf("userSystemd runtime directory has a different owner")
	}
	bus := filepath.Join(root, "bus")
	info, err := os.Lstat(bus)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("userSystemd requires the current user's local bus socket")
	}
	owner, ok = info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uid {
		return nil, fmt.Errorf("userSystemd bus has a different owner")
	}
	return map[string]string{"XDG_RUNTIME_DIR": root, "DBUS_SESSION_BUS_ADDRESS": "unix:path=" + bus}, nil
}

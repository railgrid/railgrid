#!/usr/bin/env python3
"""Refresh a private kind kubeconfig without restarting watchers on no-op writes."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile


def refresh(cluster, destination):
    data = subprocess.check_output(['kind', 'get', 'kubeconfig', '--name', cluster])
    if not data.strip():
        raise ValueError('kind returned an empty kubeconfig')
    path = Path(destination)
    if path.is_file() and not path.is_symlink() and path.read_bytes() == data:
        path.chmod(0o600)
        return
    fd, temporary = tempfile.mkstemp(prefix=path.name + '.', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as out:
            out.write(data)
            out.flush()
            os.fsync(out.fileno())
        os.replace(temporary, path)
    finally:
        Path(temporary).unlink(missing_ok=True)


if __name__ == '__main__':
    if len(sys.argv) != 3:
        sys.exit('usage: write-kind-kubeconfig.py CLUSTER DESTINATION')
    refresh(*sys.argv[1:])

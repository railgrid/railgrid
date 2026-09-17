#!/usr/bin/env python3
# Copyright 2026 The Railgrid Authors.
"""Package already-built runner binaries without credentials or enrollment.

Usage: package.py [BINDIR] [darwin|linux]
"""
import hashlib
import io
from pathlib import Path
import sys
import tarfile

# Archive names use the host label shown in the portal (macos, linux) while
# binaries keep their GOOS suffix.
BUNDLE_NAMES = {'darwin': 'macos', 'linux': 'linux'}

bindir = Path(sys.argv[1] if len(sys.argv) > 1 else 'bin')
goos = sys.argv[2] if len(sys.argv) > 2 else 'darwin'
if goos not in BUNDLE_NAMES:
    sys.exit('unsupported runner OS: ' + goos)
source = Path(__file__).parent
for arch in ('arm64', 'amd64'):
    binary = bindir / ('railgrid-runner-' + goos + '-' + arch)
    sha = hashlib.sha256(binary.read_bytes()).hexdigest()
    bundle = bindir / ('railgrid-runner-' + BUNDLE_NAMES[goos] + '-' + arch + '.tar')
    installer = ('#!/bin/sh\nset -eu\n'
                 'cd "$(dirname "$0")"\n'
                 'exec python3 ./manage.py install --binary ./railgrid-runner --sha256 ' + sha + '\n')
    with tarfile.open(bundle, 'w') as archive:
        archive.add(binary, arcname='railgrid-runner-install/railgrid-runner', recursive=False)
        archive.add(source / 'manage.py', arcname='railgrid-runner-install/manage.py', recursive=False)
        content = installer.encode()
        info = tarfile.TarInfo('railgrid-runner-install/install.sh')
        info.size, info.mode = len(content), 0o700
        archive.addfile(info, io.BytesIO(content))
    checksum = hashlib.sha256(bundle.read_bytes()).hexdigest()
    bundle.with_suffix(bundle.suffix + '.sha256').write_text(checksum + '  ' + bundle.name + '\n')
    print(bundle.name, checksum)

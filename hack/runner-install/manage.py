#!/usr/bin/env python3
# Copyright 2026 The Faros Authors.
"""Local, checksum-pinned runner installation. Never edits enrollment or state."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
import tempfile


def metadata(binary):
    result = subprocess.run([str(binary), '--version'], check=True,
                            capture_output=True, text=True, timeout=10)
    value = json.loads(result.stdout)
    arch = {'aarch64': 'arm64', 'x86_64': 'amd64'}.get(platform.machine(), platform.machine())
    if (value.get('protocolVersion') != 'runner/v1' or
            value.get('os') != platform.system().lower() or value.get('arch') != arch):
        raise ValueError('runner protocol or host architecture does not match')
    if not value.get('version') or not value.get('commit'):
        raise ValueError('runner build metadata is missing')
    return value


def digest(path):
    with path.open('rb') as stream:
        result = hashlib.sha256()
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            result.update(block)
        return result.hexdigest()


def selected(root, state, field='current'):
    sha = state.get(field, '')
    if len(sha) != 64 or any(c not in '0123456789abcdef' for c in sha):
        raise ValueError('no valid ' + field + ' installation')
    binary = root / 'releases' / sha / 'faros-runner'
    if digest(binary) != sha:
        raise ValueError('installed binary checksum mismatch')
    return binary


def save(root, state):
    fd, name = tempfile.mkstemp(dir=root, prefix='.selection-')
    try:
        with os.fdopen(fd, 'w') as stream:
            json.dump(state, stream)
            stream.write('\n')
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, root / 'selection.json')
    finally:
        Path(name).unlink(missing_ok=True)


def install(root, state, source, expected):
    if len(expected) != 64 or any(c not in '0123456789abcdef' for c in expected):
        raise ValueError('provide the trusted SHA-256 of the binary')
    releases = root / 'releases'
    releases.mkdir(exist_ok=True)
    fd, temporary = tempfile.mkstemp(dir=releases, prefix='.download-')
    try:
        with os.fdopen(fd, 'wb') as target, source.open('rb') as incoming:
            for block in iter(lambda: incoming.read(1024 * 1024), b''):
                target.write(block)
            target.flush()
            os.fsync(target.fileno())
        binary = Path(temporary)
        if digest(binary) != expected:
            raise ValueError('binary checksum mismatch; current installation unchanged')
        binary.chmod(0o700)
        info = metadata(binary)
        destination = releases / expected
        destination.mkdir(exist_ok=True)
        os.replace(binary, destination / 'faros-runner')
        if state.get('current') != expected:
            save(root, {'current': expected, 'previous': state.get('current')})
        print(json.dumps(info))
    finally:
        Path(temporary).unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=Path.home() / '.local/share/faros-runner')
    commands = parser.add_subparsers(dest='command', required=True)
    add = commands.add_parser('install')
    add.add_argument('--binary', type=Path, required=True)
    add.add_argument('--sha256', required=True)
    commands.add_parser('status')
    commands.add_parser('rollback')
    run = commands.add_parser('run')
    run.add_argument('arguments', nargs=argparse.REMAINDER)
    args = parser.parse_args()
    os.umask(0o077)
    root = args.root.expanduser().resolve()
    root.mkdir(parents=True, exist_ok=True)
    with (root / '.lock').open('a') as lock:
        # Held across exec: managed upgrades cannot race a running worker.
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        state_path = root / 'selection.json'
        state = json.loads(state_path.read_text()) if state_path.exists() else {}
        if args.command == 'install':
            install(root, state, args.binary, args.sha256)
        elif args.command == 'rollback':
            metadata(selected(root, state, 'previous'))
            save(root, {'current': state['previous'], 'previous': state['current']})
            print('Previous binary selected. Start it with the same configuration.')
        else:
            binary = selected(root, state)
            info = metadata(binary)
            if args.command == 'status':
                print(json.dumps({'binary': str(binary), **info}))
            else:
                arguments = args.arguments
                if arguments[:1] == ['--']:
                    arguments = arguments[1:]
                os.set_inheritable(lock.fileno(), True)
                os.execv(binary, [str(binary), *arguments])


if __name__ == '__main__':
    try:
        main()
    except BlockingIOError:
        sys.exit('Runner manager is busy. Drain the worker and stop the managed runner before updating.')
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        sys.exit(str(error))

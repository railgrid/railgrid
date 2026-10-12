#!/usr/bin/env python3
# Copyright 2026 The Railgrid Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Monitor Tilt's host filesystem; rotate only explicitly configured console logs.

Copy/truncate keeps a running writer's descriptor valid. A non-append writer can
leave a sparse prefix after truncation, so the limit uses allocated bytes. The
last 8 MiB is retained in one atomic .previous file; this is diagnostic retention,
not an archive. No data directories, volumes, caches, or unconfigured logs change.
"""
import argparse
import fcntl
import json
import math
import os
from pathlib import Path
import shutil
import stat
import tempfile
import time

MIB = 1024 * 1024
MAX_RETAINED_TAIL = 8 * MIB


def rotate_log(path, limit=256 * MIB, keep=8 * MIB):
    if limit <= 0:
        raise ValueError('managed log limit must be positive')
    if keep < 0 or keep > MAX_RETAINED_TAIL:
        raise ValueError(f'managed log tail must be between 0 and {MAX_RETAINED_TAIL} bytes')
    path = Path(path)
    try:
        fd = os.open(path, os.O_RDWR | os.O_NOFOLLOW | os.O_NONBLOCK)
    except FileNotFoundError:
        return False
    with os.fdopen(fd, 'r+b') as stream:
        metadata = os.fstat(stream.fileno())
        if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != os.getuid():
            raise ValueError('managed log must be a regular file owned by the current user')
        if metadata.st_nlink != 1:
            raise ValueError('managed log must not have hard links')
        fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if metadata.st_blocks * 512 <= limit:
            return False
        stream.seek(max(0, metadata.st_size - keep))
        tail = stream.read(keep)
        temporary = None
        try:
            with tempfile.NamedTemporaryFile(dir=path.parent, prefix='.tilt-log-', delete=False) as backup:
                temporary = Path(backup.name)
                backup.write(tail)
                backup.flush()
                os.fsync(backup.fileno())
            # Rename replaces an existing symlink; it never follows one.
            temporary.replace(path.with_name(path.name + '.previous'))
            temporary = None
            stream.truncate(0)
        finally:
            if temporary is not None:
                temporary.unlink(missing_ok=True)
    return True


def check_disk(paths, minimum):
    low = []
    for path in paths:
        try:
            available = shutil.disk_usage(path).free
        except OSError as error:
            low.append({'path': str(path), 'error': str(error)})
            continue
        if available < minimum:
            low.append({'path': str(path), 'availableBytes': available})
    return low


def monitored_paths(explicit):
    # Go honors GOTMPDIR first and otherwise uses the OS temporary directory.
    # Include configured cache paths too: they can reside on separate mounts.
    paths = list(explicit) or [str(Path.cwd())]
    paths.append(tempfile.gettempdir())
    paths.extend(os.environ[name] for name in ('GOTMPDIR', 'TMPDIR', 'GOCACHE')
                 if os.environ.get(name) and os.environ[name] != 'off')
    return list(dict.fromkeys(str(Path(path).absolute()) for path in paths))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--log', action='append', default=[])
    parser.add_argument('--path', action='append', default=[])
    parser.add_argument('--min-free-gib', type=float, default=5)
    parser.add_argument('--check', action='store_true', help='read-only disk readiness probe')
    args = parser.parse_args()
    if not math.isfinite(args.min_free_gib) or args.min_free_gib <= 0:
        parser.error('--min-free-gib must be a finite positive number')
    minimum_free = args.min_free_gib * 1024 ** 3
    if not math.isfinite(minimum_free):
        parser.error('--min-free-gib is too large')
    paths = monitored_paths(args.path)
    previous = None
    previous_log_errors = {}
    while True:
        if not args.check:
            log_errors = {}
            for path in args.log:
                try:
                    if rotate_log(path):
                        print(json.dumps({'event': 'tilt_log_rotated', 'path': path,
                                          'retainedTailBytes': MAX_RETAINED_TAIL}), flush=True)
                except (OSError, ValueError) as error:
                    log_errors[path] = str(error)
            if log_errors != previous_log_errors:
                event = 'tilt_log_rotation_error' if log_errors else 'tilt_log_rotation_ready'
                print(json.dumps({'event': event, 'logs': log_errors}), flush=True)
                previous_log_errors = log_errors
        low = check_disk(paths, minimum_free)
        if args.check:
            if low:
                print(json.dumps({'event': 'host_disk_low', 'filesystems': low}))
            return int(bool(low))
        state = tuple((item['path'], item.get('error')) for item in low)
        if state != previous:
            print(json.dumps({'event': 'host_disk_low' if low else 'host_disk_ready',
                              'filesystems': low}), flush=True)
            previous = state
        time.sleep(10)


if __name__ == '__main__':
    raise SystemExit(main())

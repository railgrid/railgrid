#!/usr/bin/env python3
"""Allocate stable per-checkout dev resources before Tilt opens its API socket."""
import fcntl
import hashlib
import json
import ipaddress
import re
import os
import pwd
from pathlib import Path
import socket
import subprocess
import sys
import signal
import time

ROOT = Path(__file__).resolve().parents[2]
PORTS = {'TILT_PORT': 10350, 'HUB_PORT': 9443, 'KCP_PORT': 6443,
         'KCP_ETCD_CLIENT_PORT': 2379, 'KCP_ETCD_PEER_PORT': 2380, 'PORTAL_PORT': 3000,
         'QUICKSTART_PORT': 8081, 'KROMC_PORT': 8082, 'CODE_PORT': 8083,
         'KUERY_PORT': 8084, 'APP_STUDIO_PORT': 8085, 'AGENTS_PORT': 8087,
         'EDGES_PORT': 8088, 'EDGES_INTERNAL_PORT': 8090,
         'APP_STUDIO_POSTGRES_PORT': 55432, 'KUERY_POSTGRES_PORT': 55433,
         'AGENTS_POSTGRES_PORT': 55434, 'PREVIEW_GATEWAY_PORT': 10443,
         'CODE_MINIO_PORT': 59000, 'CODE_MINIO_CONSOLE_PORT': 59001,
         'CODE_REPLICA_PORT': 18083, 'KUERY_REPLICA_PORT': 18084,
         'EDGES_REPLICA_PORT': 18088, 'EDGES_REPLICA_INTERNAL_PORT': 18090,
         'DEV_SSH_PORT': 2222, 'HA_FORWARD_PORT': 8123,
         'PLANNER_PORT': 18091, 'FACTORY_PORT': 18092, 'DATABRICKS_PORT': 18093,
         # External metering provider (railgrid/contrib-metering): the hub-facing
         # controller port, and the webhook and audit ports the embedded kcp dials.
         'METERING_PORT': 18094, 'METERING_WEBHOOK_PORT': 19443, 'METERING_AUDIT_PORT': 19444}


def ephemeral_range():
    path = Path('/proc/sys/net/ipv4/ip_local_port_range')
    if path.exists():
        return tuple(map(int, path.read_text().split()))
    # BSD/macOS use the IANA dynamic range by default. Query customized hosts.
    if sys.platform == 'darwin':
        return tuple(int(subprocess.check_output(['sysctl', '-n', key], text=True))
                     for key in ['net.inet.ip.portrange.first', 'net.inet.ip.portrange.last'])
    return (49152, 65535)


def registry_directory():
    # Coding harnesses give processes a private HOME/XDG_STATE_HOME. Port
    # claims belong to the OS account sharing the host network, not that
    # process's synthetic home. Keep ordinary checkouts and runners together.
    return Path(pwd.getpwuid(os.getuid()).pw_dir) / '.local/state/railgrid'


def allocate_ports(root, saved=None):
    registry_dir = registry_directory()
    registry_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    registry = registry_dir / 'tilt-ports.json'
    with (registry_dir / 'tilt-ports.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        entries = json.loads(registry.read_text()) if registry.exists() else {}
        # Stopped checkouts retain their claims; deleted checkouts release them.
        entries = {owner: ports for owner, ports in entries.items() if Path(owner).exists()}
        claimed = {port for owner, ports in entries.items() if owner != str(root)
                   for port in ports.values()}
        # A checkout keeps the claim it has, including the fixed ports a
        # checkout that predates per-checkout allocation holds (hub 9443, kcp
        # 6443, ...): its URLs and kubeconfigs stay stable across restarts.
        ports = dict(saved or entries.get(str(root)) or {})
        if claimed.intersection(ports.values()):
            raise ValueError('Saved Tilt ports conflict with another checkout in ' + str(registry))
        first, last = ephemeral_range()
        # Keys added to PORTS after a checkout's claim was recorded are
        # allocated on the next start; the rest of the claim is kept.
        for key in PORTS:
            if key in ports:
                continue
            for candidate in range(20000, 30000):
                if candidate in claimed or candidate in ports.values() or first <= candidate <= last:
                    continue
                with socket.socket() as sock:
                    try:
                        sock.bind(('127.0.0.1', candidate))
                    except OSError:
                        continue
                ports[key] = candidate
                claimed.add(candidate)
                break
            else:
                raise ValueError('No free Tilt port in 20000-29999 outside the host ephemeral range')
        entries[str(root)] = ports
        temporary = registry.with_suffix('.tmp')
        temporary.write_text(json.dumps(entries, indent=2) + '\n')
        temporary.chmod(0o600)
        temporary.replace(registry)
        return ports


def check_ports(config):
    for name, port in config['ports'].items():
        # A bind() probe also fails on TIME_WAIT sockets a just-stopped Tilt or
        # hub leaves behind for 2*MSL; only a listener answers a connect().
        with socket.socket() as sock:
            sock.settimeout(1)
            if sock.connect_ex(('127.0.0.1', port)) == 0:
                raise ValueError(f'{name} port {port} is unavailable for this checkout: a process is listening on it')


def settings(root=ROOT):
    state = root / '.kcp'
    state.mkdir(exist_ok=True, mode=0o700)
    path = state / 'tilt-checkout.json'
    with (state / 'tilt-config.lock').open('w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if path.exists():
            config = json.loads(path.read_text())
            if config['root'] != str(root):
                raise ValueError('Copied Tilt state belongs to another checkout; remove the copied .kcp before starting')
            allocate_ports(root, config['ports'])
            return config
        # Every checkout gets independent ports, names, and state.
        key = hashlib.sha256(str(root).encode()).hexdigest()[:12]
        config = {'root': str(root), 'key': key, 'ports': allocate_ports(root)}
        path.write_text(json.dumps(config, indent=2) + '\n')
        path.chmod(0o600)
        return config


def root_env_overrides(root):
    """Read the preview settings from the root Makefile-style .env file.

    `make tilt` imports .env itself. A direct `tilt up` does not, so the
    checkout launcher reads these two settings for the shared prepare-env
    path. Keep this deliberately narrow: the Makefile remains the loader for
    the rest of the development environment.
    """
    path = Path(root) / '.env'
    if not path.is_file():
        return {}
    overrides = {}
    pattern = re.compile(r'^\s*(?:export\s+)?(RAILGRID_APP_BASE_DOMAIN|RAILGRID_APP_PUBLIC_PORT)\s*[:?+]?=(.*)$')
    for line in path.read_text().splitlines():
        match = pattern.match(line)
        if not match:
            continue
        # Root .env is included as Make syntax, where an unescaped # starts a
        # comment. These settings are plain values; preserving an empty value
        # is meaningful for RAILGRID_APP_PUBLIC_PORT.
        value = match.group(2).split('#', 1)[0].strip()
        overrides[match.group(1)] = value
    return overrides


def preview_setting(name, source_name, default, root_env):
    """Resolve a preview setting without mistaking Tiltfile output for input.

    Tiltfile exports prepare-env values into its own process. On a file-watch
    reload, its next prepare-env subprocess inherits those values, so source
    markers tell us whether to preserve a shell setting or re-read .env/default.
    """
    is_domain = name == 'RAILGRID_APP_BASE_DOMAIN'

    def normalize(value):
        value = str(value)
        if is_domain:
            value = value.strip()
            if not value:
                return None
        return value

    def configured_file_or_default():
        if name in root_env:
            value = normalize(root_env[name])
            if value is not None:
                return value, 'dotenv'
        return str(default), 'default'

    marker = 'RAILGRID_TILT_CHECKOUT_' + source_name + '_SOURCE'
    previous_source = os.environ.get(marker)
    if previous_source == 'commandline':
        # Make supplied an explicit argument, even when it matches .env or the
        # generated default. Keep it fixed across reloads. An explicitly blank
        # domain selects the checkout default rather than restoring .env.
        value = normalize(os.environ.get(name, default))
        return (str(default) if value is None else value), 'commandline'
    if previous_source == 'environment' and name in os.environ:
        value = normalize(os.environ[name])
        if value is not None:
            return value, 'environment'
        return configured_file_or_default()
    if previous_source in ('dotenv', 'default'):
        return configured_file_or_default()

    if name in os.environ:
        inherited = normalize(os.environ[name])
        if inherited is None:
            return configured_file_or_default()
        # A Tiltfile loaded before source markers existed can already have
        # injected the generated per-checkout default. Let a newly configured
        # root .env value replace that known generated value on its first
        # reload; otherwise it would look indistinguishable from shell input.
        if is_domain and inherited == str(default):
            return configured_file_or_default()
        # GNU Make exports its resolved variables to recipes. When .env
        # supplied the same value, remember that provenance so Tilt reloads
        # can observe edits to .env instead of freezing the first value.
        if os.environ.get('MAKELEVEL') and name in root_env:
            file_value = normalize(root_env[name])
            if file_value is not None and inherited == file_value:
                return file_value, 'dotenv'
        return inherited, 'environment'
    return configured_file_or_default()


def environment(config):
    # One static development identity shared by Tilt and browser bootstrap.
    token = 'dev-token'
    auth = {'DEV_STATIC_TOKEN': token, 'DEV_STATIC_ADMIN': 'railgrid:static:' + hashlib.sha256(('static-token/' + token).encode()).hexdigest()[:16]}
    root = Path(config['root'])
    key = config['key']
    p = config['ports']
    root_env = root_env_overrides(root)
    app_base_domain, app_base_domain_source = preview_setting(
        'RAILGRID_APP_BASE_DOMAIN', 'APP_BASE_DOMAIN',
        'apps-' + key + '.127.0.0.1.sslip.io', root_env,
    )
    app_public_port, app_public_port_source = preview_setting(
        'RAILGRID_APP_PUBLIC_PORT', 'APP_PUBLIC_PORT',
        p['PREVIEW_GATEWAY_PORT'], root_env,
    )
    env = dict(auth, **{k: str(v) for k, v in p.items()})
    # Git worktrees use a .git file; some Go toolchains only recognize a
    # directory and accidentally inspect an ancestor. Make stamps our version.
    if (root / '.git').is_file():
        flags = os.environ.get('GOFLAGS', '')
        env['GOFLAGS'] = flags if '-buildvcs=false' in flags.split() else (flags + ' -buildvcs=false').strip()
    hub = f"https://console-{key}.127.0.0.1.sslip.io:{p['HUB_PORT']}"
    env.update(DEV_HUB_URL=hub, RAILGRID_HUB_EXTERNAL_URL=hub,
               RAILGRID_ACCESS_HUB_PUBLIC_URL=hub,
               HUB_INTERNAL_URL=f"https://host.docker.internal:{p['HUB_PORT']}",
               HUB_DATA_DIR=str(root / '.kcp/hub-data'),
               PORTAL_DEV_URL=f"http://localhost:{p['PORTAL_PORT']}",
               VITE_HUB_URL=f"https://localhost:{p['HUB_PORT']}",
               KRO_KIND_NAME='railgrid-kro-' + key,
               DEV_AGENT_KIND='railgrid-agent-' + key,
               KUBECONFIG=str(root / '.kcp/tilt-client.kubeconfig') + os.pathsep + str(root / '.railgrid-kro.kubeconfig'),
               RAILGRID_APP_BASE_DOMAIN=app_base_domain,
               RAILGRID_APP_PUBLIC_PORT=str(app_public_port),
               RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE=app_base_domain_source,
               RAILGRID_TILT_CHECKOUT_APP_PUBLIC_PORT_SOURCE=app_public_port_source,
               RAILGRID_TILT_KCP_LOOPBACK_PORT='6443' if os.environ.get('KCP_IMAGE') else str(p['KCP_PORT']),
               RAILGRID_TILT_CHECKOUT=key,
               KCP_EXTERNAL_CONTAINER='railgrid-kcp-' + key,
               DEV_SSH_CONTAINER='railgrid-ssh-' + key,
               HA_KUBECONFIG=str(root / ('.kubeconfig-railgrid-agent-' + key)),
               ACCESS_PROXY_IMAGE='ghcr.io/railgrid/railgrid-access-proxy:dev-' + key,
               DEV_AGENT_IMAGE='ghcr.io/railgrid/railgrid-dev-agent:dev-' + key,
               UNIVERSAL_DEV_IMAGE='ghcr.io/railgrid/railgrid-universal-dev:dev-' + key)
    for prefix, user in [('APP_STUDIO', 'appstudio'), ('AGENTS', 'agents'), ('KUERY', 'kuery')]:
        env[prefix + '_POSTGRES_CONTAINER'] = 'railgrid-' + prefix.lower().replace('_', '-') + '-pg-' + key
        env[prefix + '_DEV_DATABASE_URL'] = f"postgres://{user}:{user}@localhost:{p[prefix + '_POSTGRES_PORT']}/{user}?sslmode=disable"
    for prefix in ['QUICKSTART', 'KROMC', 'CODE', 'KUERY', 'APP_STUDIO', 'AGENTS', 'EDGES']:
        env[prefix + '_KCP_SERVER'] = f"https://localhost:{p['KCP_PORT']}"
    return env


def manifests(config, write=True):
    root = Path(config['root'])
    directory = root / '.kcp/tilt-manifests'
    if write:
        directory.mkdir(exist_ok=True)
    env = {}
    for name, prefix in [('quickstart', 'QUICKSTART'), ('infrastructure', 'KROMC'),
                         ('code', 'CODE'), ('kuery', 'KUERY'), ('app-studio', 'APP_STUDIO'),
                         ('agents', 'AGENTS'), ('edges', 'EDGES')]:
        path = directory / (name + '.yaml')
        if write:
            source = (root / 'providers' / name / 'manifest.yaml').read_text()
            port = config['ports'][prefix + '_PORT']
            old = PORTS[prefix + '_PORT']
            source, count = re.subn(rf'(?<=://)(localhost|127\.0\.0\.1):{old}(?=[/\s"\'?#]|$)',
                                    lambda match: f'{match[1]}:{port}', source)
            if count == 0:
                raise ValueError(f'providers/{name}/manifest.yaml has no loopback:{old} URL to rewrite')
            if not path.exists() or path.read_text() != source:
                path.write_text(source)
        env[prefix + '_MANIFEST'] = str(path)
    return env


def configure_host_dns(config):
    if os.environ.get('KIND_EXPERIMENTAL_PROVIDER', 'docker') != 'docker':
        raise ValueError('Checkout Tilt DNS setup requires the Docker kind provider')
    node = 'railgrid-kro-' + config['key'] + '-control-plane'
    result = subprocess.run(['docker', 'exec', node, 'getent', 'ahostsv4', 'host.docker.internal'],
                            text=True, capture_output=True)
    if result.returncode == 0 and result.stdout.strip():
        address = result.stdout.split()[0]
    else:
        nodes = json.loads(subprocess.check_output(['docker', 'inspect', node], text=True))
        networks = nodes[0].get('NetworkSettings', {}).get('Networks', {}) if nodes else {}
        network = os.environ.get('KIND_EXPERIMENTAL_DOCKER_NETWORK')
        gateways = {entry.get('Gateway') for name, entry in networks.items()
                    if entry.get('Gateway') and (not network or name == network)}
        if len(gateways) != 1:
            raise ValueError(f'Cannot resolve host.docker.internal for {node}: expected one network gateway; set KIND_EXPERIMENTAL_DOCKER_NETWORK for ambiguous networks')
        address = gateways.pop()
    address = str(ipaddress.IPv4Address(address))
    kubectl = ['kubectl', '--kubeconfig', str(Path(config['root']) / '.railgrid-kro.kubeconfig'), '-n', 'kube-system']
    cm = json.loads(subprocess.check_output(kubectl + ['get', 'configmap', 'coredns', '-o', 'json'], text=True))
    original = cm.get('data', {}).get('Corefile')
    if not original:
        raise ValueError('CoreDNS ConfigMap has no Corefile')
    corefile = re.sub(r'(?ms)^# railgrid-host-dns\n.*?^# railgrid-host-dns-end\n?', '', original)
    corefile = corefile.rstrip() + f'\n\n# railgrid-host-dns\nhost.docker.internal:53 {{\n    hosts {{\n        {address} host.docker.internal\n    }}\n}}\n# railgrid-host-dns-end\n'
    if corefile != original:
        subprocess.run(kubectl + ['patch', 'configmap', 'coredns', '--type=merge', '-p', json.dumps({'data': {'Corefile': corefile}})], check=True)
        subprocess.run(kubectl + ['rollout', 'restart', 'deployment/coredns'], check=True)
        subprocess.run(kubectl + ['rollout', 'status', 'deployment/coredns', '--timeout=120s'], check=True)


def process_identity(pid):
    # Linux runner hosts expose an unambiguous process birth time. Refuse to
    # signal by PID alone: a stale checkout must not kill a reused PID.
    if not Path('/proc').exists():
        result = subprocess.run(['ps', '-p', str(pid), '-o', 'lstart='], capture_output=True, text=True)
        return result.stdout.strip() or None
    path = Path('/proc') / str(pid) / 'stat'
    try:
        return path.read_text().rsplit(')', 1)[1].split()[19]
    except FileNotFoundError:
        return None


def owned_resources(config):
    env = environment(config)
    return {'clusters': [env['KRO_KIND_NAME'], env['DEV_AGENT_KIND']],
            'containers': [env[key] for key in (
                'KCP_EXTERNAL_CONTAINER', 'DEV_SSH_CONTAINER',
                'APP_STUDIO_POSTGRES_CONTAINER', 'AGENTS_POSTGRES_CONTAINER', 'KUERY_POSTGRES_CONTAINER')]}


def write_owner(path, record):
    temporary = path.with_suffix('.tmp')
    with os.fdopen(os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600), 'w') as out:
        os.fchmod(out.fileno(), 0o600)
        out.write(json.dumps(record) + '\n')
        out.flush()
        os.fsync(out.fileno())
    temporary.replace(path)


def stop_owned(config):
    root = Path(config['root'])
    record_path = root / '.kcp/tilt-owner.json'
    if not record_path.exists():
        return
    # Serialize with startup, including the full cleanup, so a new generation
    # cannot start between stopping processes and removing containers.
    with (root / '.kcp/tilt-owner.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        record = json.loads(record_path.read_text())
        if record.get('root') != str(root) or record.get('resources') != owned_resources(config):
            raise ValueError('Tilt ownership record does not match this checkout')
        pid = record.get('pid')
        if pid and process_identity(pid) is not None:
            if process_identity(pid) != record.get('birth'):
                raise ValueError('Tilt process identity changed; refusing to signal it')
            stop = os.killpg if os.getpgid(pid) == pid else os.kill
            stop(pid, signal.SIGTERM)
            deadline = time.monotonic() + 30
            while process_identity(pid) == record['birth'] and time.monotonic() < deadline:
                time.sleep(0.2)
            if process_identity(pid) == record['birth']:
                stop(pid, signal.SIGKILL)
        # Tilt runs every serve command in a process group of its own, so a
        # Tilt that died hard leaves them behind; they are stopped by the
        # ports they hold, before the clusters and containers go.
        stop_listeners(config)
        env = dict(os.environ, **environment(config))
        # An explicit private kubeconfig prevents kind from editing the user's
        # default kubeconfig, even after the original launcher has exited.
        env['KUBECONFIG'] = str(root / '.kcp/tilt-client.kubeconfig')
        failures = []
        def remove(args, label):
            try:
                subprocess.run(args, env=env, check=True, timeout=60)
            except (OSError, subprocess.SubprocessError):
                failures.append(label)
        for name in record['resources']['clusters']:
            remove(['kind', 'delete', 'cluster', '--name', name], 'kind cluster ' + name)
        try:
            existing = subprocess.check_output(['docker', 'ps', '-a', '--format', '{{.Names}}'], text=True, timeout=30).splitlines()
        except (OSError, subprocess.SubprocessError):
            existing = []
            failures.append('Docker container discovery')
        for name in record['resources']['containers']:
            if name in existing:
                remove(['docker', 'rm', '-f', name], 'container ' + name)
        if failures:
            raise ValueError('Cleanup incomplete; retry make dev-browser-down: ' + ', '.join(failures))
        record_path.unlink()


def release_checkout(config, record_path, keep_record=False):
    """What make tilt does once Tilt has exited, however it exited: stop what
    it left behind so the next make tilt starts clean. That is every process
    still listening on one of this checkout's ports (a serve_cmd Tilt could not
    stop) and this checkout's own containers. The kind clusters stay, because
    tilt-start reuses them; make dev-browser-down deletes them."""
    env = dict(os.environ, **environment(config))
    env['KUBECONFIG'] = str(Path(config['root']) / '.kcp/tilt-client.kubeconfig')
    failures = []
    try:
        existing = subprocess.check_output(['docker', 'ps', '-a', '--format', '{{.Names}}'], env=env, text=True, timeout=30).splitlines()
    except (OSError, subprocess.SubprocessError):
        existing = []
        failures.append('Docker container discovery')
    for name in owned_resources(config)['containers']:
        if name in existing:
            try:
                subprocess.run(['docker', 'rm', '-f', name], env=env, check=True, timeout=60,
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            except (OSError, subprocess.SubprocessError):
                failures.append('container ' + name)
    # Containers first: a published port is held by the container runtime,
    # which must never be signalled. What is left listening on a port this
    # checkout allocated is a process of this stack.
    stop_listeners(config)
    if record_path.exists() and not keep_record:
        record_path.unlink()
    if failures:
        print('Cleanup incomplete; retry with make dev-browser-down: ' + ', '.join(failures), file=sys.stderr)


def stop_listeners(config):
    """Stop every process still listening on a port this checkout allocated:
    Tilt starts each serve command in its own process group, so a Tilt that
    died hard (SIGKILL, a crash) leaves them running. The container runtime,
    which holds published ports on a container's behalf, is never signalled."""
    for name, port in sorted(config['ports'].items()):
        for pid in port_listeners(port):
            if pid == os.getpid() or is_container_runtime(pid):
                continue
            try:
                os.kill(pid, signal.SIGTERM)
            except ProcessLookupError:
                continue
            deadline = time.monotonic() + 5
            while process_identity(pid) is not None and time.monotonic() < deadline:
                time.sleep(0.2)
            if process_identity(pid) is not None:
                try:
                    os.kill(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass


def port_listeners(port):
    try:
        out = subprocess.run(['lsof', '-tiTCP:%d' % port, '-sTCP:LISTEN'], capture_output=True, text=True, timeout=30).stdout
    except (OSError, subprocess.SubprocessError):
        return set()
    return {int(pid) for pid in out.split() if pid.isdigit()}


def is_container_runtime(pid):
    try:
        comm = subprocess.run(['ps', '-p', str(pid), '-o', 'comm='], capture_output=True, text=True, timeout=10).stdout.strip()
    except (OSError, subprocess.SubprocessError):
        return True
    return any(marker in comm.lower() for marker in ('orbstack', 'docker', 'vpnkit', 'colima', 'podman'))


def tilt_settings_directory():
    # Tilt's supported override keeps the warm prepare hook independent of the
    # harness's private HOME. Match Tilt's legacy fallback without moving data.
    for key in ('TILT_DEV_DIR', 'WMDAEMON_HOME', 'WINDMILL_DIR'):
        if os.environ.get(key):
            return str(Path(os.environ[key]).resolve())
    legacy = Path.home() / '.windmill'
    return str(legacy if legacy.exists() else Path.home() / '.tilt-dev')


def main():
    if sys.argv[1:] == ['env']:
        config = json.loads((ROOT / '.kcp/tilt-checkout.json').read_text())
        if config['root'] != str(ROOT):
            raise ValueError('Tilt state belongs to another checkout')
        print(json.dumps(dict(environment(config), **manifests(config, write=False))))
        return
    config = settings()
    if sys.argv[1:] == ['status']:
        print(json.dumps(config, indent=2))
        return
    env = environment(config)
    env.update(manifests(config))
    if sys.argv[1:] == ['dns']:
        configure_host_dns(config)
        return
    if sys.argv[1:] == ['prepare-env']:
        print(json.dumps(env))
        return
    with (ROOT / '.kcp/tilt-run.lock').open('w') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError('Tilt is already running for this checkout') from None
        detached = os.environ.get('TILT_DETACHED') == '1'
        process_env = dict(os.environ, **env)
        command = ['make', 'tilt-start']
        record_path = ROOT / '.kcp/tilt-owner.json'
        with (ROOT / '.kcp/tilt-owner.lock').open('a') as owner_lock:
            fcntl.flock(owner_lock, fcntl.LOCK_EX)
            if record_path.exists():
                old = json.loads(record_path.read_text())
                if old.get('pid') and process_identity(old['pid']) is not None:
                    raise ValueError('Recorded Tilt process is still running; run make dev-browser-down first')
                # The previous stack is gone but its record is: it exited
                # without the launcher's cleanup (a detached stack, or a
                # launcher that was killed). Reap its leftovers now so they
                # cannot fail the port check below.
                if old.get('root') == str(ROOT) and old.get('resources') == owned_resources(config):
                    print('Removing leftovers of the previous Tilt stack')
                    release_checkout(config, record_path)
            check_ports(config)
            # Record intended names before launch, including partially created
            # resources when startup fails. Every name is checkout-specific.
            process_env['TILT_DEV_DIR'] = tilt_settings_directory()
            record = {'root': str(ROOT), 'resources': owned_resources(config),
                      'tiltDevDir': process_env['TILT_DEV_DIR']}
            write_owner(record_path, record)
            # A stop that arrives while Tilt is being launched is held until
            # the owner record is durable, then forwarded as a plain SIGTERM.
            pending = []
            child = None
            def terminate(signum, frame):
                if child is None:
                    pending.append(signum)
                elif child.poll() is None:
                    child.send_signal(signal.SIGTERM)
            signal.signal(signal.SIGTERM, terminate)
            signal.signal(signal.SIGINT, terminate)
            if detached:
                # The stack outlives this terminal: its own session, stdio on a
                # private log. It is stopped by make dev-browser-down, and
                # whatever it leaves behind is reaped by the next make tilt.
                log_path = ROOT / '.kcp/tilt.log'
                fd = os.open(log_path, os.O_WRONLY | os.O_CREAT | os.O_APPEND | os.O_NOFOLLOW, 0o600)
                with os.fdopen(fd, 'ab') as log:
                    os.fchmod(log.fileno(), 0o600)
                    child = subprocess.Popen(command, cwd=ROOT, env=process_env,
                                             start_new_session=True, stdin=subprocess.DEVNULL,
                                             stdout=log, stderr=subprocess.STDOUT)
            else:
                # Attached: Tilt runs in this terminal's session and foreground
                # process group, so its HUD has the controlling tty and Ctrl-C
                # reaches it directly.
                child = subprocess.Popen(command, cwd=ROOT, env=process_env)
            record.update(pid=child.pid, birth=process_identity(child.pid))
            write_owner(record_path, record)
            for signum in pending:
                terminate(signum, None)
        if detached:
            try:
                child.wait(timeout=3)
            except subprocess.TimeoutExpired:
                print('Launched owned Tilt stack; run make dev-browser-ready to verify readiness; logs: .kcp/tilt.log; stop: make dev-browser-down')
                return
            raise ValueError(f'Tilt exited during startup (status {child.returncode}); inspect .kcp/tilt.log')
        # From here the terminal delivers Ctrl-C to the whole foreground group,
        # Tilt included, so the launcher ignores it and only waits. Set after
        # the fork: a child inherits an ignored disposition across exec.
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        try:
            status = child.wait()
        finally:
            release_checkout(config, record_path)
        sys.exit(status)


def cli():
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        sys.exit(str(error))


if __name__ == '__main__':
    cli()

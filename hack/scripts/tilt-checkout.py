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
         'PLANNER_PORT': 18091, 'FACTORY_PORT': 18092, 'DATABRICKS_PORT': 18093}


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
        ports = saved or entries.get(str(root))
        if ports:
            if claimed.intersection(ports.values()):
                raise ValueError('Saved Tilt ports conflict with another checkout in ' + str(registry))
        else:
            first, last = ephemeral_range()
            ports = {}
            for key in PORTS:
                for candidate in range(20000, 30000):
                    if candidate in claimed or first <= candidate <= last:
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
    ports = config['ports']
    if config['legacy']:
        # Preserve the original launch guards; provider DBs may already run.
        ports = {key: int(os.environ.get(key, PORTS[key]))
                 for key in ['TILT_PORT', 'HUB_PORT', 'KCP_PORT']}
    for name, port in ports.items():
        with socket.socket() as sock:
            try:
                sock.bind(('127.0.0.1', port))
            except OSError as error:
                raise ValueError(f'{name} port {port} is unavailable for this checkout: {error}') from None


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
            if not config['legacy']:
                allocate_ports(root, config['ports'])
            return config
        # Existing dev installations retain their ports, names, and state.
        # Fresh checkouts always get independent resources, even if dev is down.
        legacy = (state / 'admin.kubeconfig').exists()
        key = hashlib.sha256(str(root).encode()).hexdigest()[:12]
        config = {'root': str(root), 'legacy': legacy, 'key': key,
                  'ports': PORTS if legacy else allocate_ports(root)}
        path.write_text(json.dumps(config, indent=2) + '\n')
        path.chmod(0o600)
        return config


def environment(config):
    # One static development identity shared by Tilt and browser bootstrap.
    token = 'dev-token'
    auth = {'DEV_STATIC_TOKEN': token, 'DEV_STATIC_ADMIN': 'railgrid:static:' + hashlib.sha256(('static-token/' + token).encode()).hexdigest()[:16]}
    if config['legacy']:
        return auth
    root = Path(config['root'])
    key = config['key']
    p = config['ports']
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
               RAILGRID_APP_BASE_DOMAIN='apps-' + key + '.127.0.0.1.sslip.io',
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
    if config['legacy']:
        return {}
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
    if config['legacy']:
        return
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
    if config['legacy']:
        raise ValueError('Refusing cleanup of a shared legacy Tilt stack')
    # Serialize with startup, including the full cleanup, so a new generation
    # cannot start between stopping processes and removing containers.
    with (root / '.kcp/tilt-owner.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        record = json.loads(record_path.read_text())
        if record.get('root') != str(root) or record.get('resources') != owned_resources(config):
            raise ValueError('Tilt ownership record does not match this checkout')
        pid = record.get('pid')
        if pid and process_identity(pid) is not None:
            if process_identity(pid) != record.get('birth') or os.getpgid(pid) != pid:
                raise ValueError('Tilt process identity changed; refusing to signal it')
            os.killpg(pid, signal.SIGTERM)
            deadline = time.monotonic() + 30
            while process_identity(pid) == record['birth'] and time.monotonic() < deadline:
                time.sleep(0.2)
            if process_identity(pid) == record['birth']:
                os.killpg(pid, signal.SIGKILL)
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
        check_ports(config)
        process_env = dict(os.environ, **env)
        command = ['make', 'tilt-start']
        if config['legacy']:
            result = subprocess.run(command, cwd=ROOT, env=process_env)
            sys.exit(result.returncode)
        with (ROOT / '.kcp/tilt-owner.lock').open('a') as owner_lock:
            fcntl.flock(owner_lock, fcntl.LOCK_EX)
            record_path = ROOT / '.kcp/tilt-owner.json'
            if record_path.exists():
                old = json.loads(record_path.read_text())
                if old.get('pid') and process_identity(old['pid']) is not None:
                    raise ValueError('Recorded Tilt process is still running; run make dev-browser-down first')
            # Record intended names before launch, including partially created
            # resources when startup fails. Every name is checkout-specific.
            record = {'root': str(ROOT), 'resources': owned_resources(config)}
            write_owner(record_path, record)
            pending = []
            child = None
            def terminate(signum, frame):
                if child is None:
                    pending.append(signum)
                elif child.poll() is None:
                    os.killpg(child.pid, signum)
            signal.signal(signal.SIGTERM, terminate)
            signal.signal(signal.SIGINT, terminate)
            child = subprocess.Popen(command, cwd=ROOT, env=process_env, start_new_session=True)
            record.update(pid=child.pid, birth=process_identity(child.pid))
            write_owner(record_path, record)
            for signum in pending:
                terminate(signum, None)
        sys.exit(child.wait())


def cli():
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        sys.exit(str(error))


if __name__ == '__main__':
    cli()

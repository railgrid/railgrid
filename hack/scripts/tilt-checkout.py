#!/usr/bin/env python3
"""Allocate stable per-checkout dev resources before Tilt opens its API socket."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import sys

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


def allocate_ports():
    # Hold all sockets until the whole allocation is made to prevent duplicates.
    sockets, ports = [], {}
    try:
        for key in PORTS:
            sock = socket.socket()
            sockets.append(sock)
            sock.bind(('127.0.0.1', 0))
            ports[key] = sock.getsockname()[1]
        return ports
    finally:
        for sock in sockets:
            sock.close()


def check_ports(config):
    if config['legacy']:
        return
    for name, port in config['ports'].items():
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
            return config
        # Existing dev installations retain their ports, names, and state.
        # Fresh checkouts always get independent resources, even if dev is down.
        legacy = (state / 'admin.kubeconfig').exists()
        key = hashlib.sha256(str(root).encode()).hexdigest()[:12]
        config = {'root': str(root), 'legacy': legacy, 'key': key,
                  'ports': PORTS if legacy else allocate_ports()}
        path.write_text(json.dumps(config, indent=2) + '\n')
        path.chmod(0o600)
        return config


def environment(config):
    if config['legacy']:
        return {}
    root = Path(config['root'])
    key = config['key']
    p = config['ports']
    env = {k: str(v) for k, v in p.items()}
    # Git worktrees use a .git file; some Go toolchains only recognize a
    # directory and accidentally inspect an ancestor. Make stamps our version.
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


def manifests(config):
    if config['legacy']:
        return {}
    root = Path(config['root'])
    directory = root / '.kcp/tilt-manifests'
    directory.mkdir(exist_ok=True)
    env = {}
    for name, prefix in [('quickstart', 'QUICKSTART'), ('infrastructure', 'KROMC'),
                         ('code', 'CODE'), ('kuery', 'KUERY'), ('app-studio', 'APP_STUDIO'),
                         ('agents', 'AGENTS'), ('edges', 'EDGES')]:
        source = (root / 'providers' / name / 'manifest.yaml').read_text()
        port = config['ports'][prefix + '_PORT']
        for host in ['localhost', '127.0.0.1']:
            source = source.replace(f'{host}:{PORTS[prefix + "_PORT"]}', f'{host}:{port}')
        path = directory / (name + '.yaml')
        if not path.exists() or path.read_text() != source:
            path.write_text(source)
        env[prefix + '_MANIFEST'] = str(path)
    return env


def configure_host_dns(config):
    if config['legacy']:
        return
    import ipaddress
    import re
    node = 'railgrid-kro-' + config['key'] + '-control-plane'
    result = subprocess.run(['docker', 'exec', node, 'getent', 'ahostsv4', 'host.docker.internal'],
                            text=True, capture_output=True)
    if result.returncode == 0 and result.stdout.strip():
        address = result.stdout.split()[0]
    else:
        networks = json.loads(subprocess.check_output(['docker', 'inspect', node], text=True))[0]['NetworkSettings']['Networks']
        address = networks['kind']['Gateway']
    address = str(ipaddress.IPv4Address(address))
    kubectl = ['kubectl', '--kubeconfig', str(ROOT / '.railgrid-kro.kubeconfig'), '-n', 'kube-system']
    cm = json.loads(subprocess.check_output(kubectl + ['get', 'configmap', 'coredns', '-o', 'json'], text=True))
    original = cm['data']['Corefile']
    corefile = re.sub(r'(?ms)^# railgrid-host-dns\n.*?^# railgrid-host-dns-end\n?', '', original)
    corefile = corefile.rstrip() + f'\n\n# railgrid-host-dns\nhost.docker.internal:53 {{\n    hosts {{\n        {address} host.docker.internal\n    }}\n}}\n# railgrid-host-dns-end\n'
    if corefile != original:
        subprocess.run(kubectl + ['patch', 'configmap', 'coredns', '--type=merge', '-p', json.dumps({'data': {'Corefile': corefile}})], check=True)
        subprocess.run(kubectl + ['rollout', 'restart', 'deployment/coredns'], check=True)
        subprocess.run(kubectl + ['rollout', 'status', 'deployment/coredns', '--timeout=120s'], check=True)


def main():
    config = settings()
    env = environment(config)
    env.update(manifests(config))
    if sys.argv[1:] == ['dns']:
        configure_host_dns(config)
        return
    if sys.argv[1:] == ['env']:
        print(json.dumps(env))
        return
    if sys.argv[1:] == ['status']:
        print(json.dumps(config, indent=2))
        return
    with (ROOT / '.kcp/tilt-run.lock').open('w') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError('Tilt is already running for this checkout') from None
        check_ports(config)
        process_env = dict(os.environ, **env)
        command = ['make', 'tilt-start']
        result = subprocess.run(command, cwd=ROOT, env=process_env)
        sys.exit(result.returncode)


if __name__ == '__main__':
    try:
        main()
    except ValueError as error:
        sys.exit(str(error))

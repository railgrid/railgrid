#!/usr/bin/env python3
"""Prepare a real checkout-local browser environment; never drives the browser."""
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
sys.dont_write_bytecode = True
import time
import ssl
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
_spec = importlib.util.spec_from_file_location('checkout', ROOT / 'hack/scripts/tilt-checkout.py')
checkout = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(checkout)


def write_private(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    # Replace rather than following a pre-existing link.
    temporary = path.with_suffix(path.suffix + '.tmp')
    with os.fdopen(os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600), 'w') as out:
        os.chmod(temporary, 0o600)
        out.write(value)
    temporary.replace(path)


def items(value):
    return value.get('items', [])


def provider_order(catalog, names):
    pending = set(names)
    result = []
    while pending:
        ready = sorted(n for n in pending if not {
            r['provider'] for r in catalog[n].get('requires', []) if r.get('provider')
        }.difference(result))
        if not ready:
            raise ValueError('Provider dependencies are missing or cyclic: ' + ', '.join(sorted(pending)))
        result.extend(ready)
        pending.difference_update(ready)
    return result


def enable_body(spec):
    claims, compositions = [], []
    for requirement in spec.get('requires', []):
        for resource in requirement.get('resources', []):
            name = resource['name']
            claim = {'group': requirement.get('group', ''), 'resource': name}
            claims.append(claim)
            if requirement.get('provider'):
                compositions.append(dict(claim, provider=requirement['provider']))
    return {'acceptedClaims': claims, 'acceptedCompositions': compositions,
            'acceptedHubAccess': [{'capability': a['capability'], 'scope': a['scope']}
                                  for a in spec.get('hub', {}).get('access', [])]}


class Setup:
    def __init__(self, config):
        self.config = config
        self.env = dict(os.environ, **checkout.environment(config))
        self.port = str(config['ports']['TILT_PORT'])
        self.url = checkout.environment(config)['DEV_HUB_URL']
        # Tilt's development identity, also available through its token login UI.
        self.token = 'dev-token'
        self.deadline = time.monotonic() + int(os.environ.get('DEV_BROWSER_TIMEOUT', '1200'))
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}),
            urllib.request.HTTPSHandler(context=ssl._create_unverified_context()))

    def remaining(self):
        remaining = int(self.deadline - time.monotonic())
        if remaining <= 0:
            raise TimeoutError('Development setup timed out; inspect this checkout’s Tilt logs and retry make dev-browser-ready')
        return remaining

    def command(self, args):
        result = subprocess.run(args, cwd=ROOT, env=self.env, text=True,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                timeout=self.remaining())
        if result.returncode:
            # Child output can contain credentials. Preserve it privately, not in the agent transcript.
            write_private(ROOT / '.kcp/dev-browser-error.log', result.stdout + result.stderr)
            raise ValueError(f'{args[0]} {args[1]} failed; inspect .kcp/dev-browser-error.log')
        return result.stdout

    def tilt(self, *args):
        return self.command(['tilt', *args, '--port', self.port])

    def wait_resource(self, name):
        print('Waiting for Tilt resource ' + name, flush=True)
        while True:
            self.remaining()
            try:
                resource = json.loads(self.tilt('get', 'uiresource', name, '-o', 'json'))
            except ValueError:
                # make tilt creates the kind cluster before opening its API.
                # A concurrently invoked ready command must wait for that API.
                time.sleep(min(2, self.remaining()))
                continue
            status = resource.get('status', {})
            if status.get('updateStatus') == 'error' and not status.get('queued'):
                raise ValueError(f'Tilt resource {name} failed; inspect `tilt logs {name} --port {self.port}` and retry make dev-browser-ready')
            if any(c.get('type') == 'Ready' and c.get('status') == 'True' for c in status.get('conditions', [])):
                return
            time.sleep(min(2, self.remaining()))

    def api(self, path, body=None):
        headers = {'Authorization': 'Bearer ' + self.token, 'Content-Type': 'application/json'}
        parts = path.split('/')
        if parts[1:3] == ['api', 'orgs'] and len(parts) > 3:
            headers['X-Railgrid-Org'] = parts[3]
            if len(parts) > 5 and parts[4] == 'workspaces':
                headers['X-Railgrid-Workspace'] = parts[5]
        request = urllib.request.Request(self.url + path,
            data=None if body is None else json.dumps(body).encode(),
            headers=headers)
        with self.opener.open(request, timeout=min(10, self.remaining())) as response:
            data = response.read()
            return json.loads(data) if data else {}

    def retry(self, label, fn):
        while True:
            self.remaining()
            try:
                result = fn()
                if result:
                    return result
            except urllib.error.HTTPError as error:
                if error.code not in (404, 409, 429, 500, 502, 503, 504):
                    raise ValueError(f'{label}: HTTP {error.code}; check this checkout’s hub logs') from None
            except (urllib.error.URLError, TimeoutError):
                pass
            print('Waiting for ' + label, flush=True)
            time.sleep(min(2, self.remaining()))

    def ready(self):
        self.wait_resource('(Tiltfile)')
        self.wait_resource('hub')
        resources = {r['metadata']['name'] for r in items(json.loads(self.tilt('get', 'uiresources', '-o', 'json')))}
        names = sorted(n[:-9] for n in resources if n.endswith('-register') and n[:-9] + '-init' in resources and n[:-9] in resources)
        if not names:
            raise ValueError('Tilt has no configured provider lifecycle resources')
        # Use Tilt's existing commands/environment, including external-provider checkouts.
        for name in names:
            self.wait_resource('hub')
            for action in ('register', 'init'):
                resource = name + '-' + action
                state = json.loads(self.tilt('get', 'uiresource', resource, '-o', 'json'))
                if not any(c.get('type') == 'Ready' and c.get('status') == 'True' for c in state.get('status', {}).get('conditions', [])):
                    self.tilt('trigger', resource)
                    self.wait_resource(resource)
        for name in names:
            self.wait_resource(name)
        self.retry('development login', lambda: self.api('/auth/token-login', {}))
        orgs = self.retry('personal development organization', lambda: [o for o in items(self.api('/api/orgs')) if o.get('personal')])
        org = orgs[0]['uuid']
        workspaces = self.retry('initial development workspace', lambda: [w for w in items(self.api('/api/orgs/' + org + '/workspaces')) if w.get('clusterName') and not w.get('deletionRequestedAt')])
        workspace = sorted(workspaces, key=lambda w: w['uuid'])[0]
        prefix = '/api/orgs/' + org + '/workspaces/' + workspace['uuid']
        raw = self.command(['kubectl', '--kubeconfig', str(ROOT / '.kcp/admin.kubeconfig'),
            '--server=https://localhost:' + str(self.config['ports']['KCP_PORT']) + '/clusters/root:railgrid:system:providers',
            '--insecure-skip-tls-verify', 'get', 'catalogentries', '-o', 'json'])
        catalog = {entry['metadata']['name']: entry['spec'] for entry in items(json.loads(raw))}
        missing = set(names).difference(catalog)
        if missing:
            raise ValueError('Missing registered CatalogEntries: ' + ', '.join(sorted(missing)))
        for name in provider_order(catalog, names):
            self.retry('enable ' + name, lambda name=name: self.api(prefix + '/providers/' + name + '/enable', enable_body(catalog[name])))
        def bindings_ready():
            bindings = items(self.api('/clusters/' + workspace['clusterName'] + '/apis/apis.kcp.io/v1alpha2/apibindings'))
            enabled = self.api(prefix + '/providers/enabled')['bindingNamesByProvider']
            bound = {b['metadata']['name'] for b in bindings if any(c['type'] == 'InitialBindingCompleted' and c['status'] == 'True' for c in b.get('status', {}).get('conditions', []))}
            return all(enabled.get(n) in bound for n in names)
        self.retry('provider API bindings', bindings_ready)
        token_path = ROOT / '.kcp/dev-browser-token'
        write_private(token_path, self.token + '\n')
        info = {'portalURL': self.url + '/ui/' + org + '/' + workspace['uuid'] + '/',
                'orgUUID': org, 'workspaceUUID': workspace['uuid'], 'clusterName': workspace['clusterName'],
                'tokenFile': str(token_path), 'providers': names,
                'screenshotsDirectory': 'artifacts/screenshots',
                'integrations': 'External credentials are not configured by this command. Configure only those required by the change.'}
        write_private(ROOT / '.kcp/dev-browser.json', json.dumps(info, indent=2) + '\n')
        print('Browser environment ready: ' + info['portalURL'])
        print('Use the existing token login form and tokenFile in .kcp/dev-browser.json. Do not publish credentials.')


def main():
    path = ROOT / '.kcp/tilt-checkout.json'
    if not path.exists() and sys.argv[1:] == ['down']:
        return
    deadline = time.monotonic() + 60
    while not path.exists():
        if time.monotonic() >= deadline:
            raise ValueError('Start make tilt in this checkout first; no checkout configuration appeared within 60 seconds')
        time.sleep(1)
    config = json.loads(path.read_text())
    if config.get('root') != str(ROOT) or config.get('legacy'):
        raise ValueError('Browser setup/cleanup requires a fresh isolated checkout; shared legacy stacks are not managed')
    with (ROOT / '.kcp/dev-browser.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if sys.argv[1:] == ['down']:
            checkout.stop_owned(config)
        else:
            # Never leave a stale success marker after failed setup.
            (ROOT / '.kcp/dev-browser.json').unlink(missing_ok=True)
            Setup(config).ready()


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError, KeyError) as error:
        sys.exit(str(error))

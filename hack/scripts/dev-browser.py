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

SOURCE = Path(__file__).resolve().parents[2]
ROOT = Path(os.environ.get('RAILGRID_CLEANUP_ROOT', SOURCE)).resolve()
_spec = importlib.util.spec_from_file_location('checkout', SOURCE / 'hack/scripts/tilt-checkout.py')
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
        owner_path = ROOT / '.kcp/tilt-owner.json'
        self.owner = None
        if owner_path.exists():
            owner = json.loads(owner_path.read_text())
            if owner.get('root') != str(ROOT):
                raise ValueError('Tilt ownership record does not match this checkout')
            if owner.get('tiltDevDir'):
                self.env['TILT_DEV_DIR'] = owner['tiltDevDir']
            self.owner = owner
        self.port = str(config['ports']['TILT_PORT'])
        self.url = checkout.environment(config)['DEV_HUB_URL']
        # Tilt's development identity, also available through its token login UI.
        self.token = self.env['DEV_STATIC_TOKEN']
        try:
            timeout = int(os.environ.get('DEV_BROWSER_TIMEOUT', '1200'))
            if timeout <= 0:
                raise ValueError()
        except ValueError:
            raise ValueError('DEV_BROWSER_TIMEOUT must be a positive integer number of seconds') from None
        self.deadline = time.monotonic() + timeout
        self.opener = None

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
            if self.owner and self.owner.get('pid') and (
                    not self.owner.get('birth') or checkout.process_identity(self.owner['pid']) != self.owner['birth']):
                raise ValueError('This checkout’s Tilt process has stopped; restart with make tilt TILT_DETACHED=1, then retry make dev-browser-ready')
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
        if self.opener is None:
            self.opener = urllib.request.build_opener(
                urllib.request.ProxyHandler({}),
                urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(ROOT / 'certs/apiserver.crt'))))
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

    def admin_kubectl(self, workspace, *args):
        server = 'https://localhost:' + str(self.config['ports']['KCP_PORT']) + '/clusters/' + workspace
        return self.command(['kubectl', '--kubeconfig', str(ROOT / '.kcp/admin.kubeconfig'),
                             '--server=' + server, '--insecure-skip-tls-verify', *args])

    def wait_provider(self, name):
        """Retry API queries and pending provisioning until the global setup deadline.

        Persistent query errors also retry; timeout diagnostics retain the safe
        error and private log path rather than exposing kubectl output.
        """
        # Applying a Provider is not provisioning it. Ready is written only
        # after its workspace, ServiceAccount and credential Secret exist.
        print('Waiting for provider provisioning: ' + name, flush=True)
        reason = 'controller has not reported readiness'
        while True:
            try:
                self.remaining()
                provider = json.loads(self.admin_kubectl('root:railgrid:system:providers',
                    '--request-timeout=5s', 'get', 'providers.admin.railgrid.ai', name, '-o', 'json'))
                # Reset only after a successful query. Resetting before remaining()
                # would discard the last failure when the deadline expires.
                reason = 'controller has not reported readiness'
                generation = provider.get('metadata', {}).get('generation')
                for condition in provider.get('status', {}).get('conditions', []):
                    if condition.get('type') == 'Ready':
                        if (condition.get('status') == 'True' and generation is not None
                                and condition.get('observedGeneration') == generation):
                            return
                        reason = 'Ready is false or belongs to an older generation'
            except ValueError as error:
                # command() exposes only a safe diagnostic and the private log
                # path. Retain it so auth/config failures are not hidden behind
                # a generic timeout; never print kubectl's raw stderr here.
                reason = 'provider API query failed: ' + str(error)
            except subprocess.TimeoutExpired:
                # Discovery and connections can disappear during a hub restart.
                reason = 'provider API query timed out'
            except TimeoutError:
                raise TimeoutError(f'Provider {name} provisioning timed out ({reason}); '
                    f'inspect this checkout’s hub logs with tilt logs hub --port {self.port}, '
                    'then retry make dev-browser-ready') from None
            time.sleep(min(2, max(0, self.deadline - time.monotonic())))

    def catalog(self, names):
        def read(workspace, *resource):
            return json.loads(self.admin_kubectl(workspace, 'get', *resource, '-o', 'json'))
        catalog = {entry['metadata']['name']: entry['spec'] for entry in items(read('root:railgrid:system:providers', 'catalogentries'))}
        for name in sorted(set(names).difference(catalog)):
            # Standalone external providers self-register in their own workspace,
            # which the hub watches directly. Do not duplicate their CatalogEntry
            # in system:providers or replace its runtime serving URLs.
            entry = read('root:railgrid:providers:' + name, 'catalogentries', name)
            if entry.get('metadata', {}).get('name') != name or 'spec' not in entry:
                raise ValueError('Missing registered CatalogEntry for ' + name)
            catalog[name] = entry['spec']
        return catalog

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
                    if action == 'init':
                        self.wait_provider(name)
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
        catalog = self.catalog(names)
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
                'screenshotsDirectory': str(ROOT / 'artifacts/screenshots'),
                'integrations': 'External credentials are not configured by this command. Configure only those required by the change.'}
        write_private(ROOT / '.kcp/dev-browser.json', json.dumps(info, indent=2) + '\n')
        print('Browser environment ready: ' + info['portalURL'])
        print('Use the existing token login form and tokenFile in .kcp/dev-browser.json. Do not publish credentials.')


def main():
    if sys.argv[1:] not in (['ready'], ['down']):
        raise ValueError('usage: dev-browser.py ready|down')
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
    if sys.argv[1:] == ['down']:
        # Stop resources even when setup is still polling readiness. Setup
        # creates tenant records only; it cannot restart deployment processes.
        checkout.stop_owned(config)
        return
    with (ROOT / '.kcp/dev-browser.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        # Never leave a stale success marker after failed setup.
        (ROOT / '.kcp/dev-browser.json').unlink(missing_ok=True)
        Setup(config).ready()


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError, KeyError) as error:
        sys.exit(str(error))

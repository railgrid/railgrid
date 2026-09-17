## railgrid runner run

Serve the runner/v1 protocol on a loopback listener (foreground)

### Synopsis

Serve the railgrid runner/v1 protocol on a loopback-only HTTP listener.

The runner is a single-execution coding runner for a host that is already
enrolled by its operator. Every request carries a bearer token; the listener
cannot be pointed at a non-loopback address; and the command refuses to run as
root because the harness boundaries isolate configuration, not privileges.

One runner process serves one coding harness, chosen with --harness: "codex"
(the default) or "claude" for headless Claude Code. Claude Code additionally
needs --claude-credential-file and --claude-credential-kind; the credential is
read from that file and injected into the harness child alone.

Normally an edge Addon of type "runner" supervises this command for you — see
docs/edge-addons.md. Run it by hand for a local fixture or an unmanaged host,
as described in docs/local-runner.md.

```
railgrid runner run [flags]
```

### Options

```
      --claude-binary string            Claude Code executable (default "claude")
      --claude-credential-file string   Absolute owner-only file holding the Claude Code credential (required with --harness=claude)
      --claude-credential-kind string   How to inject the Claude Code credential: oauth-token or api-key
      --claude-home string              Runner-owned CLAUDE_CONFIG_DIR directory (default <state-dir>/claude-home)
      --claude-model string             Model for Claude Code turns (empty uses the account default)
      --codex-binary string             Codex executable (default "codex")
      --codex-home string               Runner-owned CODEX_HOME directory (default <state-dir>/codex-home)
      --config string                   Path to the JSON runner enrollment/configuration file
      --harness string                  Coding harness to serve: codex or claude. One harness per runner process. (default "codex")
  -h, --help                            help for run
      --listen string                   Loopback listen address (default 127.0.0.1:8787)
      --state-dir string                Durable runner state directory
      --token-file string               File containing the runner bearer token
      --version                         Print build and protocol metadata as JSON, then exit
      --version-pin string              Expected harness version; empty uses the selected harness default (Codex 0.147.0; Claude Code unpinned)
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [railgrid runner](railgrid_runner.md)	 - Run the loopback coding runner on this host


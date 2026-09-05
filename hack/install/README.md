# hack/install — scripted install flows, shared by docs and e2e

These scripts are the single source of truth for the installation guides:

- [docs/install-external-kcp.md](../../docs/install-external-kcp.md) —
  kind → cert-manager → Envoy Gateway → etcd → kcp-operator → two-shard kcp →
  faros hub (external kcp). Scripts `01`–`07`.
- [docs/install-embedded-kcp.md](../../docs/install-embedded-kcp.md) —
  kind → (optional gateway) → faros hub with embedded kcp. Scripts `01`, `03`, `08`.

The e2e suites `test/e2e/suites/installexternal` and
`test/e2e/suites/installembedded` (`make e2e-install-external`,
`make e2e-install-embedded`) execute these scripts verbatim, so the docs stay
honest. **If you change a script, update the matching doc section — and vice
versa.**

`09-cloudflare-dns.sh` is a production-only add-on (real Cloudflare zone +
API token required) and is intentionally not covered by e2e.

Every knob is an environment variable with a sane default — see `lib.sh` for
the full contract. State (extracted kubeconfigs, port-forward pidfiles, the
generated hub token) lands in `.faros-install/` at the repo root (git-ignored).

The hub's static auth token has no fixed default: the first script you run
generates a random one, saves it to `.faros-install/hub-token` and prints it;
later scripts and re-runs reuse it. Set `FAROS_STATIC_TOKEN` to bring your own
(the e2e suites do).

Quick start (external kcp):

```bash
hack/install/01-kind-cluster.sh
hack/install/02-cert-manager.sh
hack/install/03-envoy-gateway.sh
hack/install/04-etcd.sh
hack/install/05-kcp-operator.sh
hack/install/06-kcp-shards.sh
hack/install/07-faros-hub-external.sh
hack/install/port-forward.sh start
curl -k https://localhost:9443/healthz
```

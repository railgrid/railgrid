# Home Assistant on a KubernetesCluster edge

Dev manifests that stand up Home Assistant (HA) inside an edge's Kubernetes
cluster, so the edges provider's `Service` kind can proxy to it and expose its
MCP tools to AI agents. This is the kube counterpart of running HA on a
LinuxServer host, where the agent auto-discovers it.

Loosely follows [How to run Home Assistant in Kubernetes][blog], minus the
home-lab specifics — there is no Zigbee dongle in a kind cluster, so no
`privileged`, `hostNetwork`, or `nodeSelector`, and no Ceph, so the PVC uses the
cluster's default StorageClass. The Service is ClusterIP rather than
LoadBalancer because kedge reaches HA from *inside* the cluster.

[blog]: https://blog.quadmeup.com/2025/04/07/how-to-run-home-assistant-in-kubernetes/

## What you get

| Object | Purpose |
|---|---|
| Namespace `home` | scope |
| PVC `home-assistant-config` (1Gi) | `/config` — HA's auth store, config and DB. **Losing it means redoing onboarding and reissuing tokens.** |
| Deployment `home-assistant` | `ghcr.io/home-assistant/home-assistant:stable`, port 8123, `Recreate` (RWO volume) |
| Service `home-assistant` | ClusterIP :8123 → resolves to `home-assistant.home.svc:8123` |

## Deploy

Via Tilt: click ▶ on **`ha-kube-deploy`** (it also creates the `kedge-agent`
kind cluster if it isn't up yet). Or directly:

```sh
make dev-deploy-homeassistant
```

Both apply into the `kedge-agent` kind cluster (`.kubeconfig-kedge-agent`) —
the cluster the dev kubernetes edge agent serves. First boot takes a few
minutes: it pulls a ~1.5GB image, then initialises `/config`.

## Onboard and mint a token

HA ships with no users, and you cannot create a long-lived access token without
one. Onboarding needs a browser:

```sh
make dev-homeassistant-forward     # or ▶ ha-kube-forward in Tilt
```

Then open <http://localhost:8123>, complete onboarding, and create a token at
**your profile → Security → Long-lived access tokens**.

## Wire it into kedge

Kubernetes services are **declared**, not discovered — a host has a handful of
listening ports, a cluster has hundreds of Services, so scanning them would be
noise. Two ways:

**Portal** — open the kube edge → Services → *Add service* (name, type `Home
Assistant`, target namespace `home`, target service `home-assistant`, port
`8123`), then *Connect* and paste the token.

**kubectl** — edit and apply [`kedge-service.example.yaml`](kedge-service.example.yaml)
**against your kedge tenant workspace**, not this kind cluster.

Either way the validation reconciler probes `GET /api/config` through the
tunnel; the Service goes `Ready` and fills in HA's exact version. A `401` means
the token is wrong.

## Verify

```sh
curl -H "Authorization: Bearer $USER_TOKEN" \
  "https://<hub>/services/providers/edges/edgeproxy/clusters/<cluster>/apis/edges.kedge.faros.sh/v1alpha1/services/ha-kube/proxy/api/config"
```

Then an agent granted the `edges` tool family sees `edges__ha_kube_ha_states`,
`…_ha_get_state` and `…_ha_call_service`.

## Give the agent something to open

A fresh HA has no gate. Add a demo cover to `/config/configuration.yaml` and
restart HA — then "open the gates" has a real entity to act on:

```yaml
cover:
  - platform: template
    covers:
      gate:
        friendly_name: "Gate"
        value_template: "{{ states('input_boolean.gate_open') }}"
        open_cover:
          service: input_boolean.turn_on
          target: {entity_id: input_boolean.gate_open}
        close_cover:
          service: input_boolean.turn_off
          target: {entity_id: input_boolean.gate_open}

input_boolean:
  gate_open:
    name: Gate open
```

```sh
kubectl --kubeconfig=.kubeconfig-kedge-agent -n home exec -it deploy/home-assistant -- \
  sh -c 'cat >> /config/configuration.yaml' < snippet.yaml
kubectl --kubeconfig=.kubeconfig-kedge-agent -n home rollout restart deploy/home-assistant
```

## Notes

- **Not production.** No TLS, no backups, a single replica on an RWO volume, and
  a token that can actuate anything HA can. It exists to exercise the kube
  `Service` path end to end.
- **Reverse-proxy headers.** kedge's `/svc` proxy adds `X-Forwarded-For`. HA
  ignores it by default (`use_x_forwarded_for` is off), so nothing to configure.
  If you ever turn that on to see real client IPs, you must also list the pod
  CIDR under `http.trusted_proxies` or HA will start rejecting requests.
- **Deleting the PVC resets everything** — users, tokens, entities. The kedge
  Secret would then hold a token HA no longer recognises, and the Service goes
  `Unreachable` with a `401`.

#!/usr/bin/env bash
# Step 4 — etcd for the kcp shards.
#
# A single shared etcd serves BOTH shards: each shard isolates its keyspace
# with a distinct --etcd-prefix (/shard/root, /shard/${KCP_SHARD_2} — wired in
# step 6), and the shards' embedded cache servers use their own fixed /cache
# prefix. This mirrors the upstream kcp development stack.
#
# This is a single-member, plaintext etcd suitable for development and CI.
# For production run a 3-member cluster with TLS and periodic snapshots
# (etcd-druid, the bitnami chart, or your platform's managed etcd), and put
# its client endpoint into the shard specs in step 6.

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require kubectl

kc apply -f - <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: kcp-etcd
---
apiVersion: v1
kind: Service
metadata:
  name: etcd
  namespace: kcp-etcd
spec:
  clusterIP: None
  selector:
    app: etcd
  ports:
    - name: client
      port: 2379
    - name: peer
      port: 2380
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: etcd
  namespace: kcp-etcd
spec:
  serviceName: etcd
  replicas: 1
  selector:
    matchLabels:
      app: etcd
  template:
    metadata:
      labels:
        app: etcd
    spec:
      containers:
        - name: etcd
          image: ${ETCD_IMAGE}
          command:
            - /usr/local/bin/etcd
            - --name=etcd-0
            - --data-dir=/var/lib/etcd
            - --listen-client-urls=http://0.0.0.0:2379
            - --advertise-client-urls=http://etcd-0.etcd.kcp-etcd.svc.cluster.local:2379
            - --listen-peer-urls=http://0.0.0.0:2380
            - --initial-advertise-peer-urls=http://etcd-0.etcd.kcp-etcd.svc.cluster.local:2380
            - --initial-cluster=etcd-0=http://etcd-0.etcd.kcp-etcd.svc.cluster.local:2380
            - --initial-cluster-state=new
            # kcp keeps large object counts; raise the backend quota from the
            # 2GiB default so a busy dev instance doesn't hit NO SPACE alarms.
            - --quota-backend-bytes=8589934592
          ports:
            - name: client
              containerPort: 2379
            - name: peer
              containerPort: 2380
          volumeMounts:
            - name: data
              mountPath: /var/lib/etcd
          readinessProbe:
            exec:
              command: ["/usr/local/bin/etcdctl", "endpoint", "health"]
            initialDelaySeconds: 5
            periodSeconds: 5
  volumeClaimTemplates:
    - metadata:
        name: data
      spec:
        accessModes: ["ReadWriteOnce"]
        resources:
          requests:
            storage: 8Gi
EOF

kc -n kcp-etcd rollout status statefulset/etcd --timeout=5m


# Kubernetes Node Management

This example manages the OS-level configuration of machines running Kubernetes — container runtime, kubelet, kernel tuning, CNI, and firewall rules. A rolling drain-and-reboot workflow handles OS patching without cluster disruption.

Vigo manages the OS layer underneath Kubernetes. It does not manage Kubernetes resources (deployments, configmaps, services) — that's the domain of kubectl apply and GitOps tools like ArgoCD or Flux.

## Containerd Module

Installs and configures the container runtime.

`stack/stage/modules/k8s/containerd.vgo`:

```yaml
name: k8s-containerd
vars:
  containerd_version: "1.7.*"
resources:
  - name: containerd-package
    type: package
    package: containerd.io
    state: present

  - name: containerd-config-dir
    type: directory
    path: /etc/containerd
    owner: root
    group: root
    mode: "0755"

  - name: containerd-config
    type: file
    target_path: /etc/containerd/config.toml
    owner: root
    group: root
    mode: "0644"
    content: |
      version = 2
      [plugins."io.containerd.grpc.v1.cri"]
        sandbox_image = "registry.k8s.io/pause:3.10"
        [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc]
          runtime_type = "io.containerd.runc.v2"
          [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]
            SystemdCgroup = true
    depends_on:
      - containerd-config-dir
    notify:
      - containerd-service

  - name: containerd-service
    type: service
    service: containerd
    state: running
    enabled: true
    depends_on:
      - containerd-package
```

## Kubelet Module

Configures the kubelet daemon and its systemd unit.

`stack/stage/modules/k8s/kubelet.vgo`:

```yaml
name: k8s-kubelet
depends_on:
  - k8s-containerd
vars:
  kubelet_cluster_dns: "10.96.0.10"
  kubelet_cluster_domain: "cluster.local"
  kubelet_max_pods: "110"
resources:
  - name: k8s-packages-debian
    type: package
    package: kubelet
    state: present
    when: "os_family('debian')"

  - name: k8s-packages-rhel
    type: package
    package: kubelet
    state: present
    when: "!os_family('debian')"

  - name: kubeadm-package-debian
    type: package
    package: kubeadm
    state: present
    when: "os_family('debian')"

  - name: kubeadm-package-rhel
    type: package
    package: kubeadm
    state: present
    when: "!os_family('debian')"

  - name: kubectl-package-debian
    type: package
    package: kubectl
    state: present
    when: "os_family('debian')"

  - name: kubectl-package-rhel
    type: package
    package: kubectl
    state: present
    when: "!os_family('debian')"

  - name: kubelet-config
    type: file
    target_path: /var/lib/kubelet/config.yaml
    owner: root
    group: root
    mode: "0644"
    content: |
      apiVersion: kubelet.config.k8s.io/v1beta1
      kind: KubeletConfiguration
      clusterDNS:
        - {{ .Vars.kubelet_cluster_dns }}
      clusterDomain: {{ .Vars.kubelet_cluster_domain }}
      maxPods: {{ .Vars.kubelet_max_pods }}
      cgroupDriver: systemd
      containerRuntimeEndpoint: unix:///run/containerd/containerd.sock
      rotateCertificates: true
      serverTLSBootstrap: true
    notify:
      - kubelet-service

  - name: kubelet-service
    type: service
    service: kubelet
    state: running
    enabled: true
```

## Kernel Tuning Module

Required kernel parameters and modules for Kubernetes networking and performance.

`stack/stage/modules/k8s/kernel-tuning.vgo`:

```yaml
name: k8s-kernel-tuning
resources:
  - name: br-netfilter-module
    type: file
    target_path: /etc/modules-load.d/k8s.conf
    owner: root
    group: root
    mode: "0644"
    content: |
      overlay
      br_netfilter
    notify:
      - load-kernel-modules

  - name: load-kernel-modules
    type: exec
    command: "modprobe overlay && modprobe br_netfilter"
    unless: "lsmod | grep -q br_netfilter && lsmod | grep -q overlay"

  - name: sysctl-bridge-nf-call-iptables
    type: sysctl
    key: net.bridge.bridge-nf-call-iptables
    value: "1"
    depends_on:
      - load-kernel-modules

  - name: sysctl-bridge-nf-call-ip6tables
    type: sysctl
    key: net.bridge.bridge-nf-call-ip6tables
    value: "1"
    depends_on:
      - load-kernel-modules

  - name: sysctl-ip-forward
    type: sysctl
    key: net.ipv4.ip_forward
    value: "1"

  - name: sysctl-overcommit
    type: sysctl
    key: vm.overcommit_memory
    value: "1"

  - name: sysctl-conntrack-max
    type: sysctl
    key: net.netfilter.nf_conntrack_max
    value: "131072"
    when: "has_kernel_module('nf_conntrack')"

  - name: sysctl-inotify-watches
    type: sysctl
    key: fs.inotify.max_user_watches
    value: "524288"
```

## CNI Module

Drops CNI configuration files. The actual CNI plugin (Calico, Cilium, Flannel) is typically deployed via a DaemonSet, but the host-level config directory and any bridge config must exist.

`stack/stage/modules/k8s/cni.vgo`:

```yaml
name: k8s-cni
depends_on:
  - k8s-kernel-tuning
resources:
  - name: cni-bin-dir
    type: directory
    path: /opt/cni/bin
    owner: root
    group: root
    mode: "0755"

  - name: cni-config-dir
    type: directory
    path: /etc/cni/net.d
    owner: root
    group: root
    mode: "0755"

  - name: cni-loopback-config
    type: file
    target_path: /etc/cni/net.d/99-loopback.conf
    owner: root
    group: root
    mode: "0644"
    content: |
      {
        "cniVersion": "1.0.0",
        "name": "lo",
        "type": "loopback"
      }
```

## Firewall Module

Opens the ports required by Kubernetes components.

`stack/stage/modules/k8s/firewall.vgo`:

```yaml
name: k8s-firewall
vars:
  k8s_api_port: "6443"
resources:
  # Control plane ports
  - name: fw-k8s-api
    type: firewall
    port: 6443
    protocol: tcp
    action: accept
    when: "has_executable('kubeadm')"

  - name: fw-etcd-client
    type: firewall
    port: 2379
    protocol: tcp
    action: accept
    when: "has_executable('etcd')"

  - name: fw-etcd-peer
    type: firewall
    port: 2380
    protocol: tcp
    action: accept
    when: "has_executable('etcd')"

  # Worker node ports
  - name: fw-kubelet-api
    type: firewall
    port: 10250
    protocol: tcp
    action: accept

  - name: fw-nodeport-range
    type: firewall
    port: "30000:32767"
    protocol: tcp
    action: accept
```

## Role Definition

`stack/stage/roles/k8s-node.vgo`:

```yaml
name: k8s-node
modules:
  - k8s-kernel-tuning
  - k8s-containerd
  - k8s-cni
  - k8s-kubelet
  - k8s-firewall
```

## Node Assignment

`stack/stage/envoys/nodes.vgo`:

```yaml
envoys:
  - match: "k8s-cp-*.example.com"
    environment: production
    roles: [k8s-node]
    vars:
      kubelet_max_pods: "110"

  - match: "k8s-worker-*.example.com"
    environment: production
    roles: [k8s-node]
    vars:
      kubelet_max_pods: "250"
```

## Rolling Drain-and-Reboot Workflow

For OS patching, nodes must be drained before rebooting to avoid pod disruption. This workflow drains each node, reboots it, waits for it to rejoin the cluster, and uncordons it — rolling across the fleet in batches with health checks.

### Task Definitions

`tasks/k8s-drain.yaml`:

```yaml
name: k8s-drain
description: Drain a Kubernetes node for maintenance
timeout: 300
parameters:
  node_name:
    type: string
    required: true
    description: Kubernetes node name to drain
script: |
  #!/bin/bash
  set -euo pipefail
  kubectl drain "$PARAM_NODE_NAME" \
    --ignore-daemonsets \
    --delete-emptydir-data \
    --timeout=240s \
    --force
```

`tasks/k8s-uncordon.yaml`:

```yaml
name: k8s-uncordon
description: Uncordon a Kubernetes node after maintenance
timeout: 60
parameters:
  node_name:
    type: string
    required: true
    description: Kubernetes node name to uncordon
script: |
  #!/bin/bash
  set -euo pipefail
  kubectl uncordon "$PARAM_NODE_NAME"
```

### Workflow Definition

`workflows/k8s-rolling-reboot.yaml`:

```yaml
name: k8s-rolling-reboot
description: Drain, reboot, and uncordon Kubernetes nodes in rolling batches
steps:
  - name: drain
    task: k8s-drain
    target: "k8s-worker-*.example.com"
    params:
      node_name: "{{.hostname}}"
    timeout: 300
    batch_size: "1"
    max_failures: "1"
    health_check: "kubectl get nodes -o json | jq -e '[.items[] | select(.status.conditions[] | select(.type==\"Ready\" and .status==\"True\"))] | length > 0'"

  - name: reboot
    command: "shutdown -r now"
    target: "k8s-worker-*.example.com"
    timeout: 30
    batch_size: "1"

  - name: wait-ready
    command: "kubectl wait --for=condition=Ready node/$(hostname) --timeout=300s"
    target: "k8s-worker-*.example.com"
    timeout: 360
    batch_size: "1"
    health_check: "kubectl get nodes -o json | jq -e '[.items[] | select(.status.conditions[] | select(.type==\"Ready\" and .status==\"True\"))] | length > 0'"

  - name: uncordon
    task: k8s-uncordon
    target: "k8s-worker-*.example.com"
    params:
      node_name: "{{.hostname}}"
    timeout: 60
    batch_size: "1"
```

Run with:

```bash
vigogo workflow run k8s-rolling-reboot
```

Or target a subset:

```bash
vigogo workflow run k8s-rolling-reboot --target "k8s-worker-0[1-3].example.com"
```

## Execution Order

The module DAG ensures correct ordering:

1. **k8s-kernel-tuning** — kernel modules and sysctl params (no dependencies)
2. **k8s-containerd** — container runtime (no dependencies)
3. **k8s-cni** — CNI config directories (depends on kernel-tuning)
4. **k8s-kubelet** — kubelet config and service (depends on containerd)
5. **k8s-firewall** — open required ports (no dependencies)

## What Vigo Manages vs. What It Doesn't

| Vigo manages (OS layer) | Use other tools for (cluster layer) |
|---|---|
| containerd install + config | Pod deployments, DaemonSets |
| kubelet config + service | ConfigMaps, Secrets (k8s API objects) |
| Kernel params (sysctl, modules) | Helm charts, Kustomize overlays |
| CNI host directories | CNI plugin DaemonSet (Calico, Cilium) |
| Firewall rules for k8s ports | Network policies |
| OS patching + reboot coordination | Cluster upgrades (kubeadm upgrade) |
| Node certificates (host-level PKI) | ServiceAccount tokens, RBAC |

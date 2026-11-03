<!-- vigo-i18n source_sha:e234cb91cbfbf58f3fbc1249b67ce6a863afb88d498cd0986865e96e86d94f8e -->

# Gestion des nœuds Kubernetes

Cet exemple gère la configuration au niveau du système d'exploitation des machines exécutant Kubernetes — runtime de conteneurs, kubelet, réglages du noyau, CNI et règles de pare-feu. Un workflow de drainage-et-redémarrage progressif assure les correctifs de l'OS sans perturber le cluster.

Vigo gère la couche OS sous Kubernetes. Il ne gère pas les ressources Kubernetes (déploiements, configmaps, services) — c'est le domaine de kubectl apply et des outils GitOps comme ArgoCD ou Flux.

## Configcrate Containerd

Installe et configure le runtime de conteneurs.

`stacks/configcrates/k8s/containerd.vgo` :

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

## Configcrate Kubelet


Configure le démon kubelet et son unité systemd.

`stacks/configcrates/k8s/kubelet.vgo` :

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

## Configcrate de réglage du noyau

Paramètres et modules du noyau requis pour la mise en réseau et les performances de Kubernetes.

`stacks/configcrates/k8s/kernel-tuning.vgo` :

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

## Configcrate CNI

Dépose les fichiers de configuration CNI. Le plugin CNI proprement dit (Calico, Cilium, Flannel) est généralement déployé via un DaemonSet, mais le répertoire de configuration au niveau de l'hôte et toute configuration de pont doivent exister.

`stacks/configcrates/k8s/cni.vgo` :

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

## Configcrate de pare-feu

Ouvre les ports requis par les composants Kubernetes.


`stacks/configcrates/k8s/firewall.vgo` :

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

## Définition du rôle

`stacks/roles/k8s-node.vgo` :

```yaml
name: k8s-node
configcrates:
  - k8s-kernel-tuning
  - k8s-containerd
  - k8s-cni
  - k8s-kubelet
  - k8s-firewall
```

## Affectation des nœuds


`stacks/envoys/envoys.vgo` :

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

## Workflow de drainage-et-redémarrage progressif

Pour les correctifs de l'OS, les nœuds doivent être drainés avant le redémarrage afin d'éviter toute perturbation des pods. Ce workflow draine chaque nœud, le redémarre, attend qu'il rejoigne le cluster et le décordonne — en progressant par lots à travers la flotte avec des contrôles de santé.

### Définitions des tâches

`tasks/k8s-drain.yaml` :

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

`tasks/k8s-uncordon.yaml` :

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


### Définition du workflow

`stacks/workflows/k8s-rolling-reboot.yaml` :

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

Lancez avec :

```bash
vigocli workflow run k8s-rolling-reboot
```

Ou ciblez un sous-ensemble :

```bash
vigocli workflow run k8s-rolling-reboot --target "k8s-worker-0[1-3].example.com"
```

## Ordre d'exécution

Le DAG des configcrates garantit un ordonnancement correct :

1. **k8s-kernel-tuning** — modules du noyau et paramètres sysctl (aucune dépendance)
2. **k8s-containerd** — runtime de conteneurs (aucune dépendance)
3. **k8s-cni** — répertoires de configuration CNI (dépend de kernel-tuning)
4. **k8s-kubelet** — configuration et service kubelet (dépend de containerd)
5. **k8s-firewall** — ouverture des ports requis (aucune dépendance)

## Ce que Vigo gère et ce qu'il ne gère pas

| Vigo gère (couche OS) | Utilisez d'autres outils pour (couche cluster) |
|---|---|
| Installation + configuration de containerd | Déploiements de pods, DaemonSets |
| Configuration + service kubelet | ConfigMaps, Secrets (objets de l'API k8s) |
| Paramètres du noyau (sysctl, modules) | Charts Helm, overlays Kustomize |
| Répertoires hôte CNI | DaemonSet du plugin CNI (Calico, Cilium) |
| Règles de pare-feu pour les ports k8s | Politiques réseau |
| Correctifs OS + coordination des redémarrages | Mises à niveau du cluster (kubeadm upgrade) |
| Certificats des nœuds (PKI au niveau de l'hôte) | Jetons ServiceAccount, RBAC |

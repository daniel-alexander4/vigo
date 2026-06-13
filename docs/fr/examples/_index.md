<!-- vigo-i18n source_sha:65241151b1fc5a3280026b9d642abee15330efba0a60236e62970a67e8e0c00b -->

# Exemples pas à pas

Ces exemples illustrent des configurations Vigo réelles utilisant des configcrates YAML `.vgo`, des rôles et des affectations de nœuds.

Chaque exemple est un guide pas à pas complet avec des extraits de configuration prêts à l'emploi. Adaptez les valeurs des variables et les chemins à votre environnement.

## Exemples disponibles

- [Configcrate Nginx](nginx-module.md) -- Paquet, fichier de configuration et service avec chaîne de notify
- [Configuration multi-environnements](multi-env.md) -- Production/staging avec `environments.vgo`
- [Gestion d'une stack Docker](docker-stack.md) -- Cycle de vie des conteneurs avec l'exécuteur `container`
- [Durcissement de la sécurité](security-hardening.md) -- Pare-feu, fail2ban, SSH et sudo avec `depends_on`
- [Stack de supervision](monitoring-stack.md) -- Prometheus, node_exporter et Grafana
- [Cluster de base de données](database-cluster.md) -- PostgreSQL avec des références `secret:`
- [Nœuds Kubernetes](kubernetes-nodes.md) -- Configuration de nœud k8s au niveau OS avec workflow de drain-et-redémarrage progressif
- [Générateur de bruit brun](brownnoise.md) -- image Docker construite localement et exécutée via la ressource `container:`, avec passthrough PulseAudio
- [Hygiène des disques](disk-hygiene.md) -- Prévenir la saturation des disques avec des plafonds de journal, le vieillissement de tmp, le nettoyage du cache de paquets et le pruning Docker
- [Configuration multi-cloud](multi-cloud.md) -- Configcrates adaptées au cloud utilisant les traits IMDS sur AWS, GCP et Azure

## Guides de migration

- [Vue d'ensemble de la migration](../howto/_index.md#coming-from-another-config-management-system) -- Tableau comparatif et index des guides
- [Depuis Puppet](../howto/migrate/migrate-from-puppet.md) -- Mode pull, catalogue, ressources, Facter
- [Depuis Ansible](../howto/migrate/migrate-from-ansible.md) -- Passage du push au pull, des handlers à notify
- [Depuis Chef](../howto/migrate/migrate-from-chef.md) -- DSL Ruby vers YAML, cookbooks vers configcrates
- [Depuis Salt](../howto/migrate/migrate-from-salt.md) -- YAML des deux côtés, Pillar vers vars, Grains vers Traits
- [Depuis CFEngine](../howto/migrate/migrate-from-cfengine.md) -- Théorie des promesses, classes vers when:

## Conventions

Tous les exemples suivent ces motifs :

- Les configcrates se trouvent dans `stacks/configcrates/`
- Les rôles se trouvent dans `stacks/roles/`
- Les affectations de nœuds se trouvent dans `stacks/envoys/envoys.vgo`
- Les templates Go (`{{ }}`) ne sont utilisés que dans les attributs `content:`
- Les paquets conditionnés à l'OS utilisent des ressources séparées avec des expressions `when:`
- Les secrets utilisent le préfixe `secret:` et sont résolus par le fournisseur de secrets
---
*Confidentiel -- Alexander4, LLC. Ne pas redistribuer. Voir [licence](../legal/license.md).*

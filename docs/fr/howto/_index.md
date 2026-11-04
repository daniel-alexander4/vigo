<!-- vigo-i18n source_sha:6f5ff1af7b7ddde1318d928550f9ac35decfdd1eabe1654aba7567b199d647bf -->

# Guides pratiques

Chaque page déroule une tâche d'opérateur de bout en bout. Choisissez celle qui correspond le mieux à ce que vous cherchez à faire.

## Par où commencer

Votre ordre de lecture dépend de l'état d'avancement du déploiement de votre fleet :

| Quand | Ce que vous ferez | Pages |
|---|---|---|
| **Jour 1** — première fois | Mettre en place le serveur, enrôler un seul Envoy, écrire et appliquer votre premier configcrate. | [Installation](operate/install.md) → [Enrôler un Envoy](operate/enroll-an-envoy.md) → [Écrire votre premier configcrate](configcrates/write-your-first-configcrate.md) → [Publier votre configuration](configcrates/publish-config.md) |
| **Jour 7** — exploitation de base | Authentification, supervision, accès distant. Le fleet compte désormais plus d'un opérateur et plus de quelques Envoys. | [Configurer l'authentification](operate/set-up-auth.md) → [Configurer Scrier](operate/set-up-scrier.md) → [Configurer la supervision](operate/set-up-monitoring.md) |
| **Jour 30** — production | Reporting de conformité, sauvegardes, durcissement des hôtes, les sous-systèmes de contenu Swarm. Le fleet effectue un travail réel pour de vraies parties prenantes. | [Reporting de conformité](compliance/compliance-reporting.md) → [Sauvegarder et restaurer](operate/backup-and-recovery.md) → [Durcir la mémoire des secrets de l'Envoy](operate/harden-envoy-secret-memory.md) → [Configurer Swarm](swarm/set-up-swarm.md) → [Configurer Puddle](swarm/set-up-puddle.md) → [Configurer Lockbox](swarm/set-up-lockbox.md) → [Configurer Gitback](swarm/set-up-gitback.md) |
| **Jour 90** — montée en charge et reprise après sinistre | Dimensionnement de l'hôte serveur, fédération Spanner, reprise après sinistre, distribution d'artefacts, équipements réseau, sous-systèmes personnalisés. | [Dimensionner un hôte vigosrv](operate/sizing.md) → [Configurer Spanner](operate/set-up-spanner.md) → [Reprise après sinistre](operate/disaster-recovery.md) → [Configurer Curator](swarm/set-up-curator.md) → [Configurer Longdrawer](swarm/set-up-longdrawer.md) → [Configurer les équipements réseau](operate/set-up-network-devices.md) |

Les guides de migration en bas de page s'adressent à ceux qui viennent d'un autre système de gestion de configuration — lisez-les une fois au démarrage, pas dans le flux quotidien.

## Migrer depuis un autre CM

| Guide | |
|---|---|
| [Migrer depuis Ansible](migrate/migrate-from-ansible.md) | |
| [Migrer depuis Puppet](migrate/migrate-from-puppet.md) | |
| [Migrer depuis Chef](migrate/migrate-from-chef.md) | |
| [Migrer depuis Salt](migrate/migrate-from-salt.md) | |
| [Migrer depuis CFEngine](migrate/migrate-from-cfengine.md) | |
| [Migrer depuis une version sous licence de Vigo](migrate/migrate-from-licensed-vigo.md) | Passer un serveur sous licence antérieur à 0.76.95 à la version open source |

## En cas de problème

[Résoudre les problèmes courants](operate/troubleshoot.md) est indexé par symptôme — recherchez dans la page la chaîne d'erreur exacte produite par un Envoy ou une session et accédez directement à sa solution.

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*

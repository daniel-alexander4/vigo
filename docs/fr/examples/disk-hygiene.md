<!-- vigo-i18n source_sha:837497f8ff0bb2ddd1a09092b412cd16a1ba5bce1b3ee1fbe65957201723baba -->

# Hygiène du disque

Prévenez la saturation du disque et des inodes avec un rôle qui regroupe cinq configcrates ciblés. Chaque configcrate applique une politique de nettoyage — affectez le rôle entier ou choisissez des configcrates individuels.

## Le rôle

```yaml
# stacks/roles.vgo
roles:
  - name: disk-hygiene
    configcrates: [logrotate, journal-cap, tmp-aging, package-cache, docker-prune]
```

Affectez-le à un nœud :

```yaml
match:
  - pattern: "*.web.example.com"
    roles: [bastion, disk-hygiene]
```

## Configcrates

### logrotate (existe déjà)

Installe logrotate et applique la rotation pour `/var/log/*.log` et `/var/log/*/*.log`. De nombreux configcrates de service (nginx, postgresql, haproxy, etc.) fournissent leurs propres ressources logrotate — ce configcrate couvre le filet de sécurité à l'échelle du système.

### journal-cap

Dépose une configuration systemd-journald dans `/etc/systemd/journald.conf.d/` qui plafonne la taille totale du journal et sa rétention :

```yaml
vars:
  journal_max_use: "500M"      # max disk space for journal files
  journal_max_retention: "30day"  # delete entries older than this
```

Notifie `systemd-journald` pour qu'il se recharge après les modifications.

### tmp-aging

Écrit une configuration `tmpfiles.d` qui supprime les anciens fichiers de `/tmp` et `/var/tmp` :

```yaml
vars:
  tmp_max_age: "7d"       # /tmp cleanup threshold
  var_tmp_max_age: "30d"  # /var/tmp cleanup threshold
```

Appliqué par `systemd-tmpfiles-clean.timer` (s'exécute quotidiennement sur la plupart des distributions, aucun service supplémentaire requis).

### package-cache

Conditionnel selon la famille d'OS — empêche l'accumulation de téléchargements de paquets obsolètes :

- **Debian/Ubuntu :** Définit `APT::Periodic::AutocleanInterval` pour qu'apt supprime les fichiers `.deb` remplacés tous les N jours (7 par défaut).
- **RHEL/Fedora :** Définit `keepcache=0` dans `dnf.conf` et exécute `dnf clean all` si le cache dépasse 500 Mo.

### docker-prune

Crée un timer systemd qui exécute `docker system prune -af` selon un calendrier. Supprime les conteneurs arrêtés, les images orphelines, les réseaux inutilisés et le cache de build plus ancien que `prune_age` :

```yaml
vars:
  prune_age: "168h"      # 7 days
  prune_schedule: "weekly"  # systemd OnCalendar value
```

Toutes les ressources sont conditionnées par `when: "has_command('docker')"` — le configcrate n'a aucun effet sur les machines sans Docker.

## Surcharges par nœud

Surchargez les vars depuis le bloc match ou depuis `common.vgo` :

```yaml
match:
  - pattern: "build-*.ci.example.com"
    roles: [disk-hygiene]
    vars:
      journal_max_use: "200M"
      tmp_max_age: "1d"
      prune_schedule: "daily"
      prune_age: "24h"
```

## Ce que ce rôle ne fait pas

Ce rôle applique des **politiques préventives** — il configure le système pour que l'utilisation du disque reste maîtrisée. Il ne fait pas :

- **Rechercher et supprimer des fichiers volumineux arbitraires.** Utilisez `vigocli query` pour trouver les fichiers volumineux à travers le parc lorsqu'une alerte se déclenche.
- **Alerter sur des seuils de disque/inodes.** C'est le rôle du collecteur de trait disque + l'alerte côté serveur (webhooks/SMTP).
- **Remplacer les configurations logrotate propres aux services.** Les configcrates de service (nginx, postgres, etc.) gèrent leur propre rotation des journaux. Ce rôle gère les valeurs par défaut à l'échelle du système.

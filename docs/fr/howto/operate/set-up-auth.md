<!-- vigo-i18n source_sha:0018fc18c408a1a9f17b399f7a1cdb6b7d0dde02ad7ece8466cca534ab7a2bdd -->

---
title: Authentification
---

# Configurer l'authentification

À la fin de cette page, vous disposerez d'une interface Web et d'une API REST accessibles uniquement aux utilisateurs authentifiés — soit via un nom d'utilisateur/mot de passe local (par défaut), soit via OIDC contre votre fournisseur d'identité existant, soit via `isowebauth` (le service WebAuthn hébergé d'Alexander4). Plus des jetons d'API pour l'automatisation.

**Quand l'utiliser :** chaque déploiement au-delà du premier ordinateur portable. Vous choisissez *quelle* méthode.

**Quand l'éviter :** vous ne pouvez pas — le serveur refuse par construction les requêtes non authentifiées.

Tout accès à l'interface Web et à l'API REST nécessite une authentification. Trois méthodes sont prises en charge :

| Méthode | Description | Cas d'usage |
|--------|-------------|----------|
| `basic` | Nom d'utilisateur/mot de passe (par défaut) | La plupart des déploiements |
| `oidc` | SSO OpenID Connect | SSO d'entreprise (Keycloak, Okta, Auth0, Google, Azure AD) |
| `isowebauth` | Signature par clé SSH | Équipes d'administration système utilisant des clés SSH |

## Configuration

Définissez `auth.method` dans `server.yaml` :

```yaml
auth:
  method: basic   # or: oidc, isowebauth
  session_idle_timeout: "15m"   # configurable idle timeout
```

Lorsque `method` est omis, l'authentification basic est activée par défaut.

### Délai d'inactivité de session

Les sessions expirent automatiquement après une période d'inactivité configurable. Configurez-la avec `session_idle_timeout` :

```yaml
auth:
  session_idle_timeout: "30m"   # 30 minutes; use "15m" for HIPAA
```

Définissez `"0s"` pour désactiver le délai d'inactivité (non recommandé pour les environnements réglementés).

Les changements de rôle prennent effet immédiatement. Lorsqu'un administrateur met à jour le rôle d'un autre utilisateur (`PUT /api/v1/users/{id}` ou `vigocli webusers set-role`), le serveur détruit sur-le-champ les sessions de navigateur en cache de cet utilisateur — il se réauthentifie à la requête suivante et le nouveau rôle s'applique. Il en va de même lors de la suppression d'un utilisateur. Sans cela, une rétrogradation resterait invisible pour le navigateur ouvert de l'utilisateur rétrogradé jusqu'au déclenchement du délai d'inactivité. Les jetons d'API résolvent déjà les permissions à chaque appel, ils n'ont donc besoin de rien de plus.

## Basic (nom d'utilisateur/mot de passe)

L'authentification basic stocke les comptes utilisateur dans la base de données et le mot de passe lui-même dans le coffre à secrets soumis au verrou de déverrouillage (chiffré de façon réversible sous la clé maîtresse). Le gestionnaire de connexion résout la valeur stockée à chaque tentative et la compare en temps constant au mot de passe soumis. Cette séparation tient les identifiants hors de la base de données ; le verrou de déverrouillage du coffre tient l'accès occasionnel à l'écart de la surface opérateur.

### Configuration initiale

Il n'y a pas d'utilisateur `admin` auto-amorcé ; le serveur démarre sans aucun utilisateur web. Créez votre administrateur par opérateur depuis l'hôte du serveur après avoir enrôlé l'agent local :

```bash
# First enroll the local agent so the server can verify the OS-user mapping:
curl -sSfk https://localhost:8443/bootstrap | sudo sh

# Then create yourself as the first admin. <yourname> must be a real human
# OS account on this host (uid≥1000, real shell). The CLI auto-reads your
# pubkey from ~<yourname>/.ssh/id_ed25519.pub (fallback id_rsa.pub).
sudo vigocli webusers create --username <yourname> --role admin

# Set your login password:
vigocli secrets set password vigo/web/auth/<yourname>
```

Connectez-vous sur `https://localhost:8443/login`. Les mots de passe sont stockés verbatim sous `vigo/web/auth/<username>` à l'intérieur de l'enveloppe AES-256-GCM de la clé maîtresse ; le gestionnaire de connexion effectue un `subtle.ConstantTimeCompare` contre la valeur résolue à chaque tentative.

**Les webusers admin correspondent 1:1 aux utilisateurs du système d'exploitation.** Le nom d'utilisateur côté web doit être égal à un compte humain du système d'exploitation sur l'hôte du serveur — cela rattache l'attribution d'audit à une personne réelle et amorce la clé publique pour les sessions Scrier de navigateur. Les utilisateurs viewer et compliance ne sont pas contraints ; ce sont des cas ponctuels (auditeurs, partenaires, accès en lecture seule) sans homologues sur le système d'exploitation.

### Ajouter des utilisateurs

```bash
# Another per-operator admin (real OS user required):
sudo vigocli webusers create --username alice --role admin

# Viewer / compliance — no OS-user constraint, --ssh-key-file optional:
sudo vigocli webusers create --username auditor --role viewer --email auditor@example.com
sudo vigocli webusers create --username carol --role compliance --email carol@example.com

# Set passwords:
vigocli secrets set password vigo/web/auth/alice
```

### Réinitialiser les mots de passe

```bash
vigocli secrets set password vigo/web/auth/<username>
```

### Accès CLI (jeton local-admin)

Lorsque `vigocli` s'exécute sur l'hôte du serveur, il s'authentifie avec le **jeton local-admin** — un identifiant que le serveur génère au premier démarrage et persiste sous `/srv/vigo/cli-admin-token` (mode `0600`, propriété de `root`). `vigocli` le lit automatiquement lorsqu'aucun jeton par utilisateur n'est configuré, de sorte que `sudo vigocli ...` fonctionne sur l'hôte du serveur sans configuration. L'accès CLI distant nécessite toujours un jeton d'API par utilisateur via `vigocli auth set-token`.

Le jeton est conditionné par le *système de fichiers*, et non par l'IP qui se connecte : seul un appelant capable de lire le fichier `0600` appartenant à root est admin. Un utilisateur non-root disposant d'un shell sur l'hôte du serveur — qui ne peut pas lire le fichier — obtient `401`. C'est le même verrou qui protège déjà la clé maîtresse des secrets et le binaire `vigocli` lui-même.

**Root uniquement sur le serveur.** Le binaire `vigocli` sur l'hôte du serveur est installé sous `/usr/local/bin/vigocli` avec le mode `0700` appartenant à `root:root`, et le fichier du jeton local-admin est `0600 root`. Les utilisateurs non-root ne peuvent lire ni l'un ni l'autre, donc l'accès admin reste hors de leur portée. Les chemins d'installation (`Dockerfile.server`, `entrypoint.sh`, `scripts/upgrade.sh`) imposent le mode du binaire — si vous empaquetez vigocli sur un serveur par toute autre voie, définissez le même mode à la main. Les membres du groupe `docker` sont effectivement root sur l'hôte et peuvent toujours exécuter vigocli via `docker exec` ; gardez ce groupe restreint.

## OIDC (OpenID Connect)

OIDC s'intègre à tout fournisseur d'identité conforme à OpenID Connect.

### Configuration

```yaml
auth:
  method: oidc
  oidc:
    issuer: "https://accounts.google.com"
    client_id: "your-client-id"
    client_secret: "secret:vigo/auth/oidc_client_secret"
    redirect_url: "https://vigo.example.com:8443/auth/callback"
    scopes: [openid, profile, email]
```

### Exemples de fournisseurs

**Keycloak :**
```yaml
oidc:
  issuer: "https://keycloak.example.com/realms/vigo"
  client_id: "vigo"
  client_secret: "secret:vigo/auth/oidc_client_secret"
  redirect_url: "https://vigo.example.com:8443/auth/callback"
```

**Okta :**
```yaml
oidc:
  issuer: "https://your-org.okta.com"
  client_id: "0oaXXXXXXXXXXXXX"
  client_secret: "secret:vigo/auth/oidc_client_secret"
  redirect_url: "https://vigo.example.com:8443/auth/callback"
```

**Google :**
```yaml
oidc:
  issuer: "https://accounts.google.com"
  client_id: "xxxx.apps.googleusercontent.com"
  client_secret: "secret:vigo/auth/oidc_client_secret"
  redirect_url: "https://vigo.example.com:8443/auth/callback"
```

**Azure AD :**
```yaml
oidc:
  issuer: "https://login.microsoftonline.com/{tenant-id}/v2.0"
  client_id: "your-app-id"
  client_secret: "secret:vigo/auth/oidc_client_secret"
  redirect_url: "https://vigo.example.com:8443/auth/callback"
```

### Amorçage du premier administrateur

Les utilisateurs OIDC sont créés en tant que **viewers** par défaut — il n'y a pas de promotion automatique « le premier utilisateur devient admin » (cela donnerait les droits admin à quiconque se connecte en premier sur un IdP autorisant l'auto-inscription). Accordez le premier administrateur de l'une des deux façons suivantes :

- **Amorçage opt-in (libre-service) :** nommez l'identité de l'administrateur prévu sous `oidc:` pour que sa première connexion le promeuve admin de manière déterministe :

  ```yaml
  oidc:
    # ... issuer/client_id/etc ...
    bootstrap_admin_email: "admin@example.com"   # correspondance sur la claim email
    # ou, lorsque l'email de l'IdP n'est pas stable :
    # bootstrap_admin_subject: "auth0|abc123"    # correspondance sur la claim sub
  ```

  Les deux vides (par défaut) = aucun admin automatique.

- **CLI (hors bande) :** faites connecter l'utilisateur une fois (ce qui crée sa ligne viewer), puis promouvez-le depuis l'hôte du serveur :

  ```bash
  sudo vigocli webusers set-role --username <son-email> --role admin
  ```

Les administrateurs peuvent ensuite modifier les rôles depuis la page d'administration des utilisateurs.

## Isowebauth (basé sur clé SSH)

Isowebauth utilise l'application de bureau [isowebauth](https://github.com/vigo/isowebauth) pour signer des défis avec la clé privée SSH de l'utilisateur. Le serveur vérifie les signatures à l'aide de `ssh-keygen -Y verify`.

### Configuration

1. Installez l'application de bureau isowebauth sur chaque poste de travail administrateur.
2. Configurez isowebauth pour autoriser l'origine de votre serveur Vigo et l'espace de noms `vigo`.
3. Définissez `auth.method: isowebauth` dans `server.yaml` :

```yaml
auth:
  method: isowebauth
  isowebauth:
    namespace: "vigo"   # default
```

### Gestion des utilisateurs

Contrairement à OIDC, isowebauth exige que les utilisateurs soient créés à l'avance avec leurs clés publiques SSH. Après avoir mis en place votre compte administrateur par opérateur (voir *Configuration initiale* ci-dessus), créez les autres utilisateurs depuis l'hôte du serveur :

```bash
# Viewer / compliance — --ssh-key-file accepted:
sudo vigocli webusers create --username bob --role viewer --ssh-key-file /path/to/bob.pub

# Per-operator admin — pubkey auto-read from the matching OS user's
# home dir; --ssh-key-file rejected.
sudo vigocli webusers create --username alice --role admin
```

### Flux de connexion

1. L'utilisateur saisit son nom d'utilisateur sur la page de connexion.
2. Le navigateur récupère un défi aléatoire depuis le serveur.
3. Le navigateur envoie le défi à l'application isowebauth sur `localhost:7890`.
4. Isowebauth signe le défi avec la clé SSH de l'utilisateur via `ssh-keygen -Y sign`.
5. Le navigateur renvoie la signature au serveur pour vérification.
6. Le serveur vérifie à l'aide de `ssh-keygen -Y verify` contre la clé publique stockée.
7. En cas de succès, un cookie de session est créé.

## Points de terminaison publics

Les chemins suivants sont accessibles sans authentification quelle que soit la méthode d'authentification configurée. Cela garantit que l'amorçage de l'agent, les vérifications de santé et la collecte des métriques fonctionnent sans identifiants.

| Préfixe de chemin | Objet |
|-------------|---------|
| `/bootstrap` | Script d'amorçage de l'agent et points de terminaison de certificat |
| `/api/v1/agent/` | Téléchargements du binaire de l'agent |
| `/api/v1/bootstrap/` | Enregistrement d'enrôlement de l'agent |
| `/metrics` | Collecte des métriques Prometheus |
| `/healthz` | Sonde de vérification de santé |
| `/static/` | Ressources CSS, JavaScript et images |
| `/auth/` | Gestionnaires de connexion et de rappel SSO |
| `/login` | Page de connexion |
| `/setup` | Page de configuration du premier utilisateur |

## Rôles et permissions

Trois rôles sont pris en charge :

| Action | Admin | Viewer | Compliance |
|--------|-------|--------|------------|
| Voir les tableaux de bord, Envoys, exécutions | Oui | Oui | Non |
| Voir les pages de conformité, rapports de framework | Oui | Oui | Oui |
| Téléverser la documentation de conformité justificative | Oui | Non | Oui |
| Télécharger les exports de bundle d'audit | Oui | Oui | Oui |
| Voir le journal d'audit brut (`/audit`) | Oui | Oui | **Non** |
| Voir les tâches, workflows, résultats de requête | Oui | Oui | Non |
| Dispatcher des tâches | Oui | Non | Non |
| Exécuter des workflows | Oui | Non | Non |
| Exécuter des requêtes | Oui | Non | Non |
| Gérer les motifs d'enrôlement | Oui | Non | Non |
| Gérer les webhooks | Oui | Non | Non |
| Gérer les utilisateurs | Oui | Non | Non |
| Exécuter des vérifications de santé | Oui | Non | Non |
| Créer/télécharger des sauvegardes | Oui | Non | Non |
| Créer/révoquer des jetons d'API | Oui | Oui (les siens uniquement) | Oui (les siens uniquement) |

Le rôle `compliance` est destiné au personnel GRC et aux téléverseurs de documents de conformité. Il n'est **pas destiné aux auditeurs** — les auditeurs devraient se voir attribuer `admin` ou `viewer` selon la profondeur d'accès requise. Le rôle existe pour que le personnel de conformité puisse faire son travail (téléverser BAA, plans de réponse aux incidents, dossiers de formation ; consulter les rapports de framework ; télécharger les preuves de bundle d'audit) sans bénéficier d'un accès plus large à la flotte, aux requêtes ou au journal d'audit.

### Permissions à granularité fine

Au-delà des étiquettes de rôle, chaque utilisateur dispose d'un ensemble de permissions stocké sous forme de tableau JSON. Les administrateurs disposent toujours de toutes les permissions, indépendamment du tableau stocké. Valeurs par défaut viewer : `fleet.read`, `audit.read`, `compliance.read`. Valeurs par défaut compliance : `compliance.read`, `compliance.docs.write` (pas de `audit.read`).

| Permission | Description | Admin | Viewer | Compliance |
|------------|-------------|-------|--------|------------|
| `fleet.read` | Voir les Envoys, exécutions, inventaire | Oui | Oui | Non |
| `fleet.write` | Modifier les tags d'Envoy, maintenance, mise à jour forcée | Oui | Non | Non |
| `config.publish` | Publier / geler / recharger la configuration | Oui | Non | Non |
| `config.rollback` | Revenir à une version de configuration antérieure | Oui | Non | Non |
| `push.execute` | Dispatcher des tâches ad hoc | Oui | Non | Non |
| `query.execute` | Exécuter des requêtes de traits en direct (y compris AI ask) | Oui | Non | Non |
| `workflow.execute` | Exécuter / abandonner des workflows | Oui | Non | Non |
| `users.manage` | Créer/supprimer/modifier des utilisateurs | Oui | Non | Non |
| `tokens.manage` | Gérer les jetons d'API | Oui | Non | Non |
| `webhooks.manage` | Gérer les webhooks | Oui | Non | Non |
| `backup.manage` | Créer/télécharger des sauvegardes | Oui | Non | Non |
| `enrollment.manage` | Gérer les motifs d'enrôlement | Oui | Non | Non |
| `audit.read` | Voir la chaîne du journal d'audit brut | Oui | Oui | **Non** |
| `compliance.read` | Voir les rapports de framework + bundle d'audit | Oui | Oui | Oui |
| `compliance.docs.write` | Téléverser la documentation de conformité justificative | Oui | Non | Oui |
| `compliance.docs.purge` | Supprimer définitivement les documents de conformité supprimés en douceur | Oui | Non | Non |

L'exclusion de `audit.read` sur le rôle compliance est délibérée : le journal d'audit brut à chaîne de hachage est réservé à admin et viewer, tandis que l'export de bundle d'audit (un artefact de preuve de conformité par framework construit à partir des mêmes données) repose sur `compliance.read`, de sorte que le rôle peut télécharger ce qu'un auditeur demande sans se voir confier l'intégralité du flux d'audit.

Des ensembles de permissions personnalisés peuvent être attribués par utilisateur via l'API REST pour adapter l'accès en dehors des trois rôles nommés (p. ex., un opérateur capable de dispatcher des tâches mais pas de gérer les utilisateurs, ou un viewer pouvant également publier la configuration).

## Authentification à deux facteurs (TOTP)

TOTP (Time-based One-Time Password) ajoute un second facteur à l'authentification basic (HIPAA 164.312(d)). Lorsqu'elle est activée, les utilisateurs doivent saisir un code à 6 chiffres provenant d'une application d'authentification après leur mot de passe.

### Configuration

1. Connectez-vous à l'interface web avec votre mot de passe
2. Appelez l'API de configuration TOTP :
   ```bash
   curl -X POST https://vigo:8443/api/v1/users/totp/setup \
     -H "Authorization: Bearer $TOKEN"
   ```
3. Scannez l'`url` renvoyée sous forme de code QR dans votre application d'authentification (Google Authenticator, Authy, 1Password, etc.)
4. Confirmez avec un code valide :
   ```bash
   curl -X POST https://vigo:8443/api/v1/users/totp/confirm \
     -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"code": "123456"}'
   ```
5. Conservez les codes de récupération renvoyés dans un emplacement sécurisé

### Codes de récupération

Huit codes de récupération sont générés lorsque TOTP est activé. Chaque code ne peut être utilisé qu'une seule fois. Si vous perdez l'accès à votre application d'authentification, saisissez un code de récupération à la place d'un code TOTP à l'invite de connexion.

### Désactiver TOTP

```bash
curl -X POST https://vigo:8443/api/v1/users/totp/disable \
  -H "Authorization: Bearer $TOKEN"
```

### Points de terminaison TOTP

| Point de terminaison | Méthode | Description |
|----------|--------|-------------|
| `/api/v1/users/totp/setup` | POST | Générer le secret TOTP (renvoie le secret + l'URL otpauth) |
| `/api/v1/users/totp/confirm` | POST | Confirmer la configuration avec un code valide, active TOTP |
| `/api/v1/users/totp/disable` | POST | Désactiver TOTP pour votre compte |

## Jetons d'API

Les jetons d'API permettent à la CLI et aux scripts de s'authentifier sans session de navigateur. Les jetons sont hachés au repos.

### Créer un jeton

Via la CLI (nécessite une session active ou un jeton existant) :

```bash
# Create a token (the plaintext is shown once):
vigocli auth set-token $(curl -s -X POST \
  -H "Authorization: Bearer $EXISTING_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name": "ci-deploy", "expires_in": "720h"}' \
  https://vigo.example.com:8443/api/v1/auth/tokens | jq -r .token)
```

Ou directement via l'API REST :

```bash
curl -X POST https://vigo.example.com:8443/api/v1/auth/tokens \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name": "my-token", "expires_in": "720h"}'
```

### Authentification CLI


Stockez un jeton pour l'usage de la CLI :

```bash
vigocli auth set-token <token>
vigocli auth status    # verify
vigocli auth clear     # remove
```

Le jeton est stocké de manière sécurisée dans le répertoire personnel de l'utilisateur.

### Points de terminaison de jeton

| Point de terminaison | Méthode | Description |
|----------|--------|-------------|
| `/api/v1/auth/tokens` | POST | Créer un nouveau jeton |
| `/api/v1/auth/tokens` | GET | Lister vos jetons (métadonnées uniquement) |
| `/api/v1/auth/tokens/{id}` | DELETE | Révoquer un jeton |

## Gestion des utilisateurs

### API REST

| Point de terminaison | Méthode | Auth | Description |
|----------|--------|------|-------------|
| `/api/v1/users` | GET | Admin | Lister tous les utilisateurs |
| `/api/v1/users` | POST | Admin | Créer un utilisateur |
| `/api/v1/users/{id}` | PUT | Admin | Mettre à jour un utilisateur |
| `/api/v1/users/{id}` | DELETE | Admin | Supprimer un utilisateur |

### CLI

```bash
vigocli webusers list
vigocli webusers create --username alice --role admin [--email alice@example.com] [--ssh-key-file ~/.ssh/id.pub]
vigocli secrets set password vigo/web/auth/alice
vigocli webusers delete --username alice
```

## Journalisation d'audit

Tous les événements d'authentification et de gestion des utilisateurs sont enregistrés dans la chaîne d'audit infalsifiable (table `audit_entries`). Ces événements prennent en charge les contrôles d'audit HIPAA 164.312(b) et la détection d'intrusion.

**Convention de type d'événement.** Chaque type d'événement d'audit est `<domain>.<resource>.<action>` (trois niveaux) lorsque le domaine comporte plusieurs ressources qu'il vaut la peine de désambiguïser, sinon `<domain>.<action>` (deux niveaux). En minuscules, séparé par des points. Exemples ci-dessous.

| Type d'événement | Déclencheur | Acteur |
|------------|---------|-------|
| `auth.login` | Connexion réussie (basic, OIDC, isowebauth) | Nom d'utilisateur |
| `auth.login_failed` | Échec de connexion (mauvais mot de passe, utilisateur inconnu, mauvaise signature) | Nom d'utilisateur tenté |
| `auth.logout` | Déconnexion de l'utilisateur | Nom d'utilisateur |
| `user.create` | Compte utilisateur créé | Admin qui a créé |
| `user.delete` | Compte utilisateur supprimé | Admin qui a supprimé |
| `user.role_change` | Rôle mis à jour (uniquement lorsque le rôle change réellement) | Admin qui a modifié |
| `user.totp_setup` | Secret TOTP généré pour l'utilisateur (avant confirmation) | Nom d'utilisateur concerné |
| `user.totp_enable` | TOTP activé après confirmation du code par l'opérateur | Nom d'utilisateur concerné |
| `user.totp_disable` | TOTP désactivé | Nom d'utilisateur concerné |
| `token.create` | Jeton d'API créé | Propriétaire du jeton |
| `token.revoke` | Jeton d'API révoqué | Propriétaire du jeton |
| `secret.accessed` | Secret révélé via `vigocli secrets reveal` | Utilisateur CLI (via l'en-tête `X-Vigo-User`) |
| `secret.rotation_acked` | Le serveur a accusé réception d'une rotation de secret signalée par `vigocli secrets set` | Utilisateur de la session |
| `secrets.unlocked` | Coffre à secrets déverrouillé | Utilisateur de la session |
| `secrets.locked` | Coffre à secrets verrouillé | Utilisateur de la session |
| `secrets.passphrase_rotated` | Phrase secrète de déverrouillage tournée | Utilisateur de la session |
| `secrets.passphrase_reset` | Phrase secrète de déverrouillage réinitialisée via break-glass | Utilisateur de la session |
| `spanner.id_initialized` | Le Bolt fondateur a initialisé le Spanner | pubkey-hex du fondateur |
| `spanner.join_offered` / `.join_accepted` / `.join_rejected` | La demande d'admission du Bolt rejoignant a été proposée / acceptée / rejetée | pubkey-hex de l'arrivant |
| `spanner.bolt_admitted` / `.bolt_invite_issued` / `.bolt_push` / `.bolt_drain` / `.peer_reassign` | Verbes de contrôle de la fédération Spanner | Utilisateur de la session opérateur |

Les autres domaines d'événements suivent la même convention : `compliance.docs.*` (upload/replace/delete/purge/link_added/link_removed/download), `compliance.bundle.download`, `curator.s3_credential.{create,revoke}`, `db.{vacuum,checkpoint,prune,analyze}`, `envoy.{enroll,delete,revoke,force_update,force_update_all,maintenance_set,maintenance_cleared}`, `config.{reload,publish_recorded,rollback,freeze,unfreeze,retract,published}`, `task.{dispatch,cancel,cancel_running,blocked}`, `workflow.{dispatch,abort}`, `sandgorgon.{commission,decommission,return_to_available,provision,import_csv,image.register,image.remove}`, `swarm.{cleanup,curator.block,curator.unblock,poolq.block,poolq.unblock,filecast.distribute,filecast.revoke,filecast.denied}`, `peer.promote`, `scrier.connect`, `security.hardening_warning`, `webusers.legacy_resigned`, `emergency.access`.

> **Avis de renommage (2026-05-25).** La taxonomie des événements d'audit a été unifiée en notation hiérarchique pointée dans cette version. Les entrées de chaîne écrites avant le renommage conservent leur orthographe d'origine (la chaîne de hachage SHA-256 est append-only par conception). Noms historiques que vous pourriez trouver dans d'anciennes lignes de chaîne : `spanner_id_initialized` → `spanner.id_initialized`, `spanner_join_offered` → `spanner.join_offered`, `spanner_join_accepted` → `spanner.join_accepted`, `spanner_join_rejected` → `spanner.join_rejected`, `spanner_bolt_admitted` → `spanner.bolt_admitted`, `spanner_bolt_push` → `spanner.bolt_push`, `spanner_bolt_drain` → `spanner.bolt_drain`, `spanner_peer_reassign` → `spanner.peer_reassign`, `spanner_bolt_invite_issued` → `spanner.bolt_invite_issued`, `secret_accessed` → `secret.accessed`, `secrets_unlocked` → `secrets.unlocked`, `secrets_locked` → `secrets.locked`, `secrets_passphrase_rotated` → `secrets.passphrase_rotated`, `secrets_passphrase_reset` → `secrets.passphrase_reset`, `compliance.waiver_created` → `compliance.waiver.created`, `compliance.waiver_revoked` → `compliance.waiver.revoked`, `compliance.docs.link.add` → `compliance.docs.link_added`, `compliance.docs.link.remove` → `compliance.docs.link_removed`. `vigocli audit list --type` ne traduit pas — interrogez le nom d'origine pour trouver les entrées historiques, le nouveau nom pour les entrées postérieures au renommage.

Les événements de connexion et d'échec de connexion incluent l'adresse IP du client dans le champ de charge utile (`{"ip":"..."}`) pour la détection des attaques par force brute. Les événements d'échec de connexion enregistrent le nom d'utilisateur tenté (pas le mot de passe).

Consulter les événements d'audit :

```bash
vigocli audit list
vigocli audit list --type auth.login_failed
```

## Dépannage

**Le rappel OIDC échoue avec « invalid state parameter » :**
La session a peut-être expiré entre la redirection et le rappel. Vérifiez que `redirect_url` correspond exactement à ce qui est enregistré auprès de votre fournisseur OIDC.

**Isowebauth « Cannot connect to isowebauth app » :**
Assurez-vous que l'application de bureau isowebauth est en cours d'exécution et écoute sur `127.0.0.1:7890`. Vérifiez que l'origine de votre serveur Vigo figure dans les origines autorisées d'isowebauth.

**« ssh-keygen: command not found » :**
Le serveur nécessite `ssh-keygen` dans le PATH pour la vérification des signatures isowebauth. Installez OpenSSH sur le serveur.

**L'API renvoie 401 avec un jeton valide :**
Vérifiez que le jeton n'a pas expiré. Listez vos jetons avec `vigocli auth status` ou l'API des jetons.

## Et ensuite

- **Ajoutez des clés publiques SSH par utilisateur** pour que les sessions Scrier les détectent automatiquement → [Configurer Scrier](set-up-scrier.md).
- **Auditez qui s'est connecté et quand** → chaque événement d'authentification atterrit dans le [journal d'audit à chaîne de hachage](../compliance/compliance-reporting.md).
- **L'authentification se comporte mal** → [Dépanner les problèmes courants](troubleshoot.md).

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*

*Confidentiel -- Alexander4, LLC. Ne pas redistribuer. Voir [licence](../../legal/license.md).*

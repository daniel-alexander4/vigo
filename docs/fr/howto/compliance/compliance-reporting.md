<!-- vigo-i18n source_sha:f951da3e3caa36e0b0e766dc6db3d3acf713793ff06b4875954f5b0be065499a -->

# Rapports de conformité

À la fin de cette page, vous disposerez de tags `compliance:` au niveau des configcrates, agrégés en couverture par framework que les auditeurs peuvent lire directement — produits en JSON ou HTML sur la CLI, ou consultés sur l'interface Web `/compliance`. Les chiffres proviennent d'un seul endroit : les configcrates déclarent quels contrôles ils satisfont, le serveur en fait l'union sur l'ensemble de la flotte (plus les vérifications de traits Connwaer et `waivers.vgo`), et chaque surface lit depuis la même jointure. Pas de tableurs, pas de comptabilité par contrôle en dehors du fichier configcrate lui-même.

**Quand l'utiliser :** chaque fois que vous avez besoin d'un artefact pour un auditeur externe (HIPAA, SOC 2, PCI, HITRUST, CIS), d'alimenter une plateforme GRC en preuves (Vanta / Drata / Secureframe / Laika / Thoropass / ServiceNow), ou de répondre « quels contrôles notre flotte satisfait-elle ? » à partir d'une seule commande.

**Quand l'éviter :** les contrôles que Vigo ne peut honnêtement attester (sécurité physique, paperasse BAA, revues de fournisseurs) — déclarez-les dans `waivers.vgo` et documentez la justification ; ne taggez pas un configcrate qui ne peut pas réellement prouver le contrôle.

## Taggez vos configcrates

Chaque configcrate qui satisfait un contrôle reçoit un bloc `compliance:`. Utilisez les véritables identifiants de contrôle du framework :

```yaml
# /srv/vigo/stacks/configcrates/openvpn.vgo
name: openvpn
compliance:
  hipaa:       ["164.312(e)(1)", "164.312(e)(2)(ii)"]
  pci-dss:     ["2.3", "4.1"]
  nist-800-53: ["SC-8", "SC-8(1)", "SC-13"]
  iso-27001:   ["A.13.1.1", "A.13.2.1"]
  cis-ubuntu:  ["3.5.1.1", "3.5.1.2"]
resources:
  - …
```

Consultez la [matrice de conformité](../../reference/compliance-matrix.md) pour le tableau canonique contrôle → couverture Vigo ; utilisez-le pour déterminer quels configcrates satisfont quoi.

Un contrôle est considéré comme couvert pour un Envoy lorsque **l'une** des conditions suivantes est vraie :

1. L'Envoy a un configcrate taggé avec ce contrôle.
2. Une vérification de trait Connwaer l'escalade au statut satisfait.
3. Une dérogation s'applique (déclarée dans `waivers.vgo`).

## Examinez la couverture

L'interface Web est le bon endroit pour *parcourir* la couverture. La CLI est le bon endroit pour *produire* l'artefact de l'auditeur.

**Interface Web :** `https://vigo:8443/compliance` — cartes par framework avec % de couverture, exploration détaillée jusqu'à l'état par contrôle et l'attribution par Envoy.

![Tableau de bord de conformité avec cartes de couverture par framework](../../img/screenshots/vigo.compliance.png)

Chaque carte est cliquable ; la page par framework montre quels contrôles sont couverts, lesquels ne le sont pas, et quel Envoy constitue actuellement le maillon le plus faible.

![Vue par framework — liste de contrôles avec attribution de couverture et agrégation par Envoy](../../img/screenshots/vigo.compliance-frameworks.png)

## Produisez un rapport pour un auditeur

Un verbe par framework, avec `--format html` ou `--format json` et `--output <file>` :

```bash
vigocli compliance report compliance --format html --output hipaa-2026-Q2.html
vigocli compliance report soc2       --format html --output soc2-2026-Q2.html
vigocli compliance report pci        --format html --output pci-2026-Q2.html
vigocli compliance report hitrust    --format html --output hitrust-2026-Q2.html
vigocli compliance report cis        --format html --output cis-2026-Q2.html
```

(Le verbe HIPAA est `compliance` plutôt que `hipaa` pour des raisons historiques — HIPAA était le framework d'origine, le nom générique porte donc ce mappage. Les autres verbes correspondent à leurs frameworks de manière biunivoque.)

Pour un bundle inviolable qui associe le rapport à la tranche pertinente du journal d'audit :

```bash
vigocli compliance report audit-bundle hipaa --output hipaa-audit-2026-Q2.zip
```

Les rapports HTML sont rendus côté serveur ; les rapports JSON vous donnent les mêmes données mises en forme pour l'ingestion. La sortie inclut :

- Un résumé exécutif avec le % de couverture global par framework.
- Un tableau par contrôle : identifiant de contrôle, description, statut (couvert / partiel / non couvert / dérogé), configcrates satisfaisants, Envoys attestants, horodatage de la dernière convergence.
- Pour `audit-bundle` : un extrait du journal d'audit à chaîne de hachage sur la fenêtre du rapport.

## Alimentez une plateforme GRC

Exportateurs sortants pour les principales plateformes GRC :

```bash
vigocli compliance export siem  --output evidence.jsonl
vigocli compliance export cmdb  --output cmdb.json
vigocli compliance export oscal --output oscal.json
```

Pour un envoi continu (plutôt qu'un export ponctuel), configurez l'intégration GRC dans `server.yaml` — Vigo POSTe les preuves selon un planning.

## Envoi vers une plateforme GRC (Vanta, Drata, Secureframe, Laika, Thoropass, ServiceNow GRC)

Les exportateurs GRC de Vigo formatent les preuves réussite/échec par contrôle et les POSTent vers votre plateforme GRC selon un planning. Configuration dans `server.yaml` :

```yaml
grc:
  enabled: true
  vendor: vanta              # or drata | secureframe | laika | thoropass | servicenow | generic
  endpoint: "https://api.vanta.com/v1/..."
  token: "secret:grc/vanta/token"
  push_interval: "1h"
  frameworks: [soc2, iso-27001]
```

Consultez `reference/server-yaml.md` pour le schéma complet par fournisseur.

## Dérogations

Certains contrôles ne peuvent pas être satisfaits par l'automatisation (contrôles de processus, contrôles de sécurité physique, exercices PCA). Déclarez une dérogation :

```yaml
# /srv/vigo/stacks/waivers.vgo
waivers:
  - control: "pci-dss:9.1"           # physical access to cardholder data env
    rationale: "Co-located in SOC-2 audited DC; vendor-provided physical security."
    expires: "2027-04-01"
    owner: "compliance@example.com"
  - control: "nist-800-53:CP-4"      # contingency-plan testing
    rationale: "Annual DR exercise per BCP-2026-03; next exercise 2026-09."
    expires: "2027-03-01"
    owner: "compliance@example.com"
```

Les dérogations apparaissent dans l'attribution par contrôle comme la raison de la couverture. Les dérogations arrivant à expiration déclenchent une notification 30 jours avant l'échéance.

## Rôles et accès

- **Le rôle Admin** voit tout et peut exporter chaque rapport.
- **Le rôle Compliance** (`compliance.read` + `compliance.docs.write` par défaut) voit l'interface de conformité et peut téléverser des artefacts de portée documentée ; il ne peut délibérément pas lire le journal d'audit brut (`audit.read` est réservé à admin/viewer).
- **Le rôle Viewer** voit la couverture mais pas les données d'audit brutes.

## Listes de contrôle réglementaires par framework

La matrice des frameworks vous indique quels contrôles chaque framework exige et quels configcrates Vigo les satisfont. Les listes de contrôle ci-dessous sont un travail de préparation côté opérateur — quelles preuves rassembler, quelles dérogations déposer, quelles conversations avoir avec l'auditeur — qui va au-delà de ce que Vigo applique directement.

### Liste de contrôle HIPAA Security Rule

1. **Inventoriez les flux PHI.** Listez chaque Envoy qui touche aux PHI ; taggez-les avec un label hostcrate `phi: true`. Vigo n'infère pas la portée PHI — vous la déclarez.
2. **§164.312(a)(1) contrôle d'accès.** Exécutez les configcrates `user` et `sudo` ; taggez-les avec le contrôle HIPAA pertinent. Vérifiez avec `vigocli compliance report compliance --format html --output verify.html` et explorez la section par Envoy.
3. **§164.312(b) contrôles d'audit.** Le journal d'audit à chaîne de hachage de Vigo satisfait l'exigence des « mécanismes d'audit » ; exportez avec `vigocli audit export --since 2026-01-01 --to 2026-03-31 --format pdf`.
4. **§164.312(c) contrôles d'intégrité.** Configcrates de surveillance de l'intégrité des fichiers ; associez à la vérification WORM de Connwaer si vous l'utilisez.
5. **§164.312(e)(1) sécurité des transmissions.** Le mTLS est appliqué par construction ; documentez avec `vigocli compliance report compliance`.
6. **Business Associate Agreement.** Hors de la portée de Vigo. Maintenez-le dans votre dépôt de contrats ; faites un lien depuis le rapport de conformité.
7. **Évaluation annuelle des risques.** La page `/risk` de Vigo produit un scoring à l'échelle de la flotte ; exportez avec `vigocli risk report --format pdf`.

### Liste de contrôle SOC 2 Type II

1. **Définissez la portée de vos trust services criteria.** Sécurité, Disponibilité, Intégrité du traitement, Confidentialité, Vie privée — choisissez ceux qui sont dans le périmètre et taggez les configcrates en conséquence.
2. **Contrôles Common Criteria (CC).** La plupart des contrôles CC correspondent au durcissement de la plateforme (jeu de configcrates `security-hardening`), à la surveillance (`set-up-monitoring`) et à l'audit (le journal d'audit intégré). Taggez et exportez.
3. **Exigence de surveillance continue.** L'intervalle de convergence de Vigo, de 15 secondes à 5 minutes, EST votre surveillance continue ; documentez l'intervalle et le comportement de correction de dérive dans votre narratif SOC 2.
4. **Gestion des changements.** `vigocli config publish` produit une piste d'audit à chaîne de hachage ; exportez-la pour l'échantillon de gestion des changements de l'auditeur.
5. **Organisations de sous-service.** Hors de la portée de Vigo.
6. **Test d'intrusion annuel.** Hors de la portée de Vigo ; les résultats alimentent le rapport en tant que preuves.

### Liste de contrôle PCI-DSS

1. **Cadrage du CDE.** Taggez chaque Envoy dans l'environnement des données de porteur de carte (`cde: true`). Les configcrates qui ciblent les Envoys du CDE portent le tag de conformité `pci-dss:`.
2. **Exigence 1 — pare-feu.** Vigo ne remplace pas votre pare-feu ; il peut vérifier l'état du pare-feu hôte via l'exécuteur `firewall` ou la vérification de segmentation réseau de Connwaer.
3. **Exigence 2 — valeurs par défaut.** Le configcrate `security-hardening` désactive les comptes par défaut et change les mots de passe par défaut ; recoupez avec `vigocli compliance report pci` et explorez la section par Envoy.
4. **Exigence 6 — développement sécurisé.** La provenance des builds de Vigo (signatures binaires, attestations de chaîne d'approvisionnement) est capturée dans le journal d'audit.
5. **Exigence 10 — journalisation.** Acheminement d'audit sécurisé par mTLS vers un SIEM ; la configuration `integrations` couvre Splunk, Datadog, syslog générique.
6. **Scans ASV trimestriels.** Hors de la portée de Vigo.

### Liste de contrôle NY DFS Cyber (23 NYCRR 500)

1. **§500.02 programme de cybersécurité.** La matrice des frameworks de Vigo est l'artefact.
2. **§500.05 tests d'intrusion.** Hors de la portée de Vigo ; alimentez le rapport avec les résultats.
3. **§500.06 piste d'audit.** Journal d'audit à chaîne de hachage ; rétention configurable via `audit.retention_days` dans `server.yaml`.
4. **§500.09 évaluation des risques.** Le moteur de scoring de risque de Vigo ; exportez avec `vigocli risk report`.
5. **§500.11 politique tiers.** Hors de la portée de Vigo.
6. **§500.16 réponse aux incidents.** Les actions `notify` de Vigo + les intégrations poussent vers PagerDuty/Slack/ServiceNow — documentez dans votre IRP.

### Mise hors service d'un Envoy (piste d'audit)

1. **Révoquez d'abord, supprimez ensuite.** `vigocli envoys revoke <id>` — l'Envoy ne peut plus s'enregistrer, mais l'historique reste.
2. **Effacez l'état sensible** si vous réutilisez physiquement le matériel : `vigo swarm puddle leave --yes` sur l'Envoy (si une appartenance à un Puddle existait) ; procédure d'effacement standard NIST 800-88 pour le disque.
3. **Documentez la mise hors service** avec un extrait final de rapport de conformité — exécutez les verbes de rapport par framework qui vous intéressent et archivez la sortie HTML avec le ticket de mise hors service.
4. **Supprimez l'enregistrement de l'Envoy** après votre fenêtre de rétention : `vigocli envoys delete <id>`.

### Liste de contrôle de réponse aux incidents

1. **Détection.** Alertes de dérive, page `/risk`, recherche d'impact CVE — tous font remonter les anomalies. Configurez les intégrations `drift-alerting` et `cve-impact` pour alimenter PagerDuty / Slack.
2. **Triage avec Scrier.** `vigocli scrier ssh <suspect-envoy>` — aucun port entrant nécessaire, chaque session est journalisée dans l'audit.
3. **Confinement.** `vigocli envoys quarantine <id>` révoque l'enregistrement de l'Envoy et met la convergence en pause ; s'inverse proprement avec `unquarantine`.
4. **Capture forensique.** `vigocli envoys snapshot <id>` exporte l'intégralité des traits + l'historique des exécutions + la chaîne de résolution de configuration de l'Envoy au moment de l'incident.
5. **Réinitialisez et réenrôlez.** Si l'Envoy est compromis : effacez et re-bootstrappez ; le nouvel Envoy obtient une nouvelle paire de clés ED25519, de sorte que l'hôte compromis ne peut pas l'usurper lors du réenrôlement.
6. **Post-mortem.** Extrait du journal d'audit sur la fenêtre de l'incident ; joignez-le à votre ticket d'incident.

## Et ensuite

- **Tagger un configcrate** → consultez la [matrice de conformité](../../reference/compliance-matrix.md) pour savoir quels contrôles chaque framework attend et quels exécuteurs Vigo / vérifications Connwaer les satisfont.
- **Pousser des preuves vers une plateforme GRC selon un planning** → configurez la section GRC dans [`server.yaml`](../../reference/server-yaml.md).
- **Un contrôle ne peut pas être satisfait par l'automatisation** → déposez une dérogation dans `waivers.vgo` avec une justification et une expiration ; le rapport l'étiquette tout de même couvert-par-dérogation plutôt que non couvert.
- **Un verbe de rapport échoue ou les chiffres semblent faux** → [Dépanner les problèmes courants](../operate/troubleshoot.md).

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*

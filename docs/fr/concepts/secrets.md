<!-- vigo-i18n source_sha:b51979f5ee69e03a65042bd335c65c45010ff72fbf8a45fff79eb5d9192a6c13 -->

# Secrets

**Comment un secret entre-t-il dans une configuration sans jamais y figurer ?**

Vous le référencez. Partout où une valeur peut apparaître dans un fichier `.vgo`, vous écrivez `secret:<path>` au lieu de la valeur littérale — `dsn: "secret:vigo/db/dsn"`. Une référence peut aussi être **intégrée à l'intérieur d'une valeur plus grande**, y compris plusieurs dans une seule chaîne composée — `env: "PW=secret:vigo/db/pw,DB=app"` résout le jeton `secret:vigo/db/pw` sur place et laisse le reste littéral (une référence s'étend jusqu'au premier caractère qui ne fait pas partie d'un chemin). Valeur entière ou intégrée, chaque référence est validée au chargement et force-poussée lors de la rotation. Vigo résout la valeur réelle au dernier moment possible, sur l'agent, à l'instant de l'application, et jamais avant.

## Le chemin qu'un secret ne prend *pas*

L'essentiel à comprendre, c'est tous les endroits où le texte en clair **n'est pas** :

1. **Au chargement de la configuration**, le serveur vérifie que chaque référence `secret:` existe dans le backend — mais substitue un marqueur opaque (`__VIGO_SECRET__<path>__END__`) dans la configuration résolue. La valeur réelle n'est jamais stockée sur l'objet de configuration de l'Envoy.
2. **Dans le PolicyBundle** envoyé à l'agent, seul le marqueur circule. Le texte en clair ne traverse jamais le réseau.
3. **À l'instant de l'application**, l'agent appelle `VigoAgent.ResolveSecrets` pour les valeurs dont son bundle a réellement besoin. Un Envoy ne peut résoudre que les références dont le marqueur apparaît dans *son propre* bundle — une récupération non autorisée échoue avec `PermissionDenied` et émet un événement d'audit.
4. Les valeurs résolues vivent dans le **cache en mémoire** de l'agent uniquement pour la durée de vie du processus. Un champ `secrets_epoch` s'incrémente à chaque rechargement et à chaque rotation, invalidant le cache.

Ainsi, un secret n'apparaît jamais en clair dans la configuration, les variables d'environnement, les journaux, la base de données, les charges utiles gRPC ou les résultats d'exécution. C'est une contrainte ferme et non négociable.

## Backends

Le backend est enfichable, résolu au chargement de la configuration (échoue immédiatement s'il est injoignable) :

- **`local`** (par défaut) — les chemins correspondent à des fichiers chiffrés en AES-256-GCM sous `<secrets-dir>`, chiffrés sous une clé maîtresse. Le chemin de lecture du serveur est autonome à travers les redémarrages.
- **`isopass`** (production) — un fournisseur de secrets HTTP.

La surface opérateur (`vigocli secrets {init,unlock,rotate,set,reveal,…}`) est protégée par déverrouillage ; le chemin de lecture autonome du serveur ne l'est pas.

## Où cela apparaît

- [Gérer les secrets](../reference/vigocli/secrets.md) — la surface CLI.
- [Durcir la mémoire des secrets](../howto/operate/harden-envoy-secret-memory.md) — verrouiller le cache en RAM de l'agent.
- [Le modèle de sécurité](security-model.md) — où se situent les secrets dans la posture globale.

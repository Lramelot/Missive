# Journal des modifications

[English](CHANGELOG.md)

Les changements notables de Missive sont listés ici. Les versions suivent le
[versionnage sémantique](https://semver.org/lang/fr/) ; le format s'inspire de
[Keep a Changelog](https://keepachangelog.com/fr/).

## [Non publié]

## [0.2.0] - 2026-11-18

### Ajouté

- L'interface existe en anglais et en français. Elle suit la langue du système, et un réglage
  « Langue » la change sans redémarrer.
- La recherche accepte les mots-clés anglais (`from:`, `in:`, `has:`, `before:`, `after:`) en plus
  des français, dans les deux langues.
- Les exports de conversation et les messages d'erreur du bridge suivent la langue de l'app.

### Corrigé

- Les messages reçus pendant la mise en veille de l'ordinateur apparaissent dans la conversation
  ouverte, et plus seulement dans l'aperçu de la barre latérale.
- Au réveil ou après une reconnexion, les conversations récentes dont la dernière activité dépasse
  le dernier message connu récupèrent les messages manquants.
- L'ouverture d'une conversation récupère les messages plus récents que le cache local.
- L'aperçu de la barre latérale suit le message enregistré le plus récent, même quand Meta signale
  une activité sans envoyer le message.
- La galerie de médias n'échoue plus avec « invalid type: null » sur certaines conversations.

### Modifié

- La release macOS est compilée sur des machines Intel et peut être relancée seule pour un tag
  existant.
- L'intégration continue ne compile plus les tags, déjà couverts par le workflow de release.

## [0.1.18] - 2026-11-17

Première version publique.

### Ajouté

- Client Messenger natif pour Windows et macOS : interface en Rust (GPUI) et bridge en Go basé sur
  mautrix-meta.
- Conversations chiffrées de bout en bout, avec restauration de l'historique par le code PIN
  Messenger.
- Réponses, réactions, mentions, modification et retrait, transfert, sondages, messages épinglés,
  liens enrichis, photos, vidéos, fichiers, positions, messages vocaux et emojis.
- Envoi programmé, rappels, recherche avancée, favoris, dossiers, réponses rapides, heures calmes,
  conversations prioritaires, lecture discrète, export et envoi hors connexion.
- Notifications Windows avec réponse directe, badge de non-lus, fenêtre détachable, démarrage avec
  Windows, mises à jour automatiques, thèmes clair et sombre.
- Les GIF s'animent directement dans la conversation. Les vidéos affichent une vignette avec leur
  durée et s'ouvrent dans le lecteur du système.
- Un clic sur l'avatar d'une conversation à deux ouvre le profil Facebook de la personne dans le
  navigateur par défaut.
- L'icône de Missive est intégrée à l'exécutable (barre des tâches, raccourcis).
- Après une erreur inattendue, Missive redémarre seul, au plus une fois par minute.

### Corrigé

- Se reconnecter après une déconnexion ne fait plus planter l'app. La première tentative de
  connexion n'échoue plus.
- La fenêtre de connexion est plus large, pour que les bannières de cookies de Meta tiennent.
- Le champ du code PIN Messenger accepte au plus 6 chiffres.
- Faire défiler les Paramètres, le code PIN, la galerie ou la visionneuse ne fait plus défiler la
  conversation derrière.
- La barre de défilement est toujours visible. Les Paramètres gardent leur bouton de fermeture en
  haut pendant que leur contenu défile.
- Ouvrir la fenêtre détachée ne fait plus planter l'app.

[Non publié]: https://github.com/Lramelot/Missive/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/Lramelot/Missive/compare/v0.1.18...v0.2.0
[0.1.18]: https://github.com/Lramelot/Missive/releases/tag/v0.1.18

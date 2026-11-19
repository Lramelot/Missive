# Journal des modifications

[English](CHANGELOG.md)

Les changements notables de Missive sont listés ici. Les versions suivent le
[versionnage sémantique](https://semver.org/lang/fr/) ; le format s'inspire de
[Keep a Changelog](https://keepachangelog.com/fr/).

## [Non publié]

## [0.3.0] - 2026-11-19

### Ajouté

- Un message supprimé par son expéditeur reste lisible, avec « Ce message a été supprimé » au-dessus.
  Un message qui arrive déjà supprimé affiche toujours « Message retiré ». Les exports suivent la
  même règle.
- Si le bridge s'arrête de façon inattendue, Missive le relance tout seul. Après plusieurs échecs, un
  écran d'erreur propose un bouton Relancer et affiche la fin du journal technique.

### Corrigé

- Dans les conversations chiffrées de bout en bout en tête-à-tête, un message supprimé ou modifié par
  l'autre personne est désormais réellement supprimé ou modifié. Avant, le changement était ignoré.
- Un bridge qui ne répond plus ne bloque plus l'app : chaque requête a désormais un délai maximal, et
  les images et avatars continuent de se charger après un incident.
- Après une longue coupure réseau, Missive continue d'essayer de se reconnecter (au plus toutes les
  5 minutes) au lieu d'abandonner après 10 essais. Les conversations chiffrées réessaient aussi.
- Une valeur inconnue dans le fichier de réglages (après un retour à une version précédente, par
  exemple) ne réinitialise plus tous les réglages : les réglages valides sont conservés et une copie
  du fichier est gardée.
- Les messages de la même milliseconde ne sont plus perdus au chargement de l'historique ancien.
- L'indicateur « en train d'écrire… » disparaît de lui-même si l'événement de fin se perd.
- Les fenêtres de conversation détachées se mettent à jour après une modification, une réaction ou
  un vote.

### Modifié

- Les médias téléchargés, miniatures et photos de profil passent dans un cache local
  (`%LOCALAPPDATA%\Missive\cache`), hors du profil itinérant. Miniatures et avatars sont plafonnés à
  500 Mo.
- Les dates et heures suivent la langue de l'interface. Le raccourci de recherche affiche ⌘K sur
  macOS.
- Les messages consécutifs d'une même personne sont regroupés quand moins de 5 minutes les séparent.
- Le bandeau du message épinglé montre un aperçu du message.
- Moins de travail disque et processeur : réglages et brouillons ne sont écrits que s'ils changent,
  jamais sur le fil de l'interface ; les conversations que vous n'avez pas ouvertes depuis un moment
  libèrent leurs anciens messages en mémoire.

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

[Non publié]: https://github.com/Lramelot/Missive/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/Lramelot/Missive/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/Lramelot/Missive/compare/v0.1.18...v0.2.0
[0.1.18]: https://github.com/Lramelot/Missive/releases/tag/v0.1.18

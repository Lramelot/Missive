# Packaging (Velopack)

Installation et mises à jour reposent sur [Velopack](https://docs.velopack.io). Le paquet contient
`missive` et `missive-bridge` (le bridge reste à côté de l'appli). Identifiant de paquet : `Missive`,
AppUserModelID Windows : `Missive.Desktop` (nécessaire aux notifications). La version vient de
`[workspace.package]` dans le `Cargo.toml` racine. Les données utilisateur
(`%APPDATA%\Missive`, `~/Library/Application Support/Missive`) ne sont jamais touchées.

Tout est généré dans `target/packaging/` (ignoré par Git).

## Prérequis

- Rust stable, Go 1.26+
- SDK .NET et l'outil `vpk`, dans la même version que la crate : `dotnet tool install -g vpk --version 1.2.161`
- macOS : Xcode (outils en ligne de commande), cibles Rust `aarch64-apple-darwin` et `x86_64-apple-darwin`

## Build local

Windows :

```powershell
./packaging/build-windows.ps1
```

Compile le bridge (`CGO_ENABLED=0`) et l'appli, prépare `target/packaging/windows/pack`, puis lance
`vpk pack` (canal `win`) vers `target/packaging/windows/releases`. Sans `vpk`, le script s'arrête après
la préparation et explique comment l'installer.

macOS :

```bash
./packaging/build-macos.sh
```

Compile les deux architectures, fusionne avec `lipo`, assemble `Missive.app` (identifiant
`app.missive.desktop`, bridge dans `Contents/MacOS/`), signe en ad hoc, puis lance `vpk pack` (canal
`osx`) vers `target/packaging/macos/releases`. Une icône `packaging/missive.icns` ou
`packaging/missive.ico` est utilisée si elle existe.

Pour produire des deltas en local, définir `MISSIVE_UPDATE_REPO=owner/nom` (et `GITHUB_TOKEN` si le
dépôt est privé) : la dernière release est téléchargée avant `vpk pack`.

## Releases et mises à jour

1. Mettre à jour la version dans `Cargo.toml`, puis pousser un tag `vX.Y.Z` identique.
2. `.github/workflows/release.yml` compile avec `MISSIVE_UPDATE_URL` (l'adresse du dépôt GitHub, lue
   par l'appli), télécharge la release précédente pour calculer les deltas, lance `vpk pack`, puis
   `vpk upload github --publish --merge`. Le job macOS passe après le job Windows et fusionne ses
   fichiers dans la même release.
3. L'appli interroge la release GitHub, télécharge le delta en arrière-plan, et l'applique au
   redémarrage.

Canaux : `win` pour Windows, `osx` pour macOS. Chaque canal a son propre flux (`releases.<canal>.json`).
La première release n'a pas de release précédente : l'échec du téléchargement est ignoré.

## Raccourcis

`vpk pack --shortcuts Desktop,StartMenuRoot --aumid Missive.Desktop` : l'installeur crée
`Bureau\Missive.lnk` et `Menu Démarrer\Programmes\Missive.lnk`, avec l'AppUserModelID
`Missive.Desktop` posé sur le raccourci (sans lui, Windows n'affiche pas les notifications de
l'appli).

Velopack ne crée les raccourcis qu'à l'installation. À chaque mise à jour, il recherche les
raccourcis existants qui pointent dans `%LOCALAPPDATA%\Missive` et les met à jour, mais ne recrée
jamais un raccourci absent : un raccourci supprimé, quelle qu'en soit la cause, ne revient pas
(journal : `%LOCALAPPDATA%\velopack\velopack_Missive.log`, lignes `Will update all current
shortcuts`).

Au démarrage d'une version installée, Missive vérifie donc le raccourci du menu Démarrer : absent,
il est recréé (cible `%LOCALAPPDATA%\Missive\Missive.exe`, AppUserModelID `Missive.Desktop`) ;
présent et pointant vers l'appli mais sans le bon AppUserModelID, celui-ci est rétabli. Le
raccourci du Bureau n'est jamais recréé, pour respecter une suppression volontaire.

## Notifications

Les notifications proposent « Répondre… », « Envoyer » et « Marquer comme lu ». Limite connue :
l'appli n'enregistre pas d'activateur COM, les actions passent par l'événement `Activated` du
processus en cours. Une réponse tapée depuis le Centre de notifications après la disparition de la
bannière, ou après un redémarrage de Missive, peut ne pas parvenir à l'appli.

## Journaux

L'appli écrit sa sortie d'erreur (dont les lignes transmises par le bridge) dans
`%LOCALAPPDATA%\Missive\logs\missive.log` (`missive-demo.log` en mode démo), horodatée en UTC,
avec rotation à 2 Mo sur 3 fichiers (`missive.1.log`, `missive.2.log`). Paramètres, Diagnostic :
« Copier le diagnostic » et « Ouvrir le dossier des journaux ».

## Signature

- Windows : `MISSIVE_SIGN_PARAMS` est transmis à `vpk pack --signParams` (signtool), et
  `MISSIVE_AZURE_SIGN_FILE` à `--azureTrustedSignFile` (Azure Trusted Signing).
- macOS : la signature ad hoc ne suffit pas pour la distribution. Avec un compte Apple Developer,
  définir `MISSIVE_SIGN_IDENTITY` (certificat « Developer ID Application ») et `MISSIVE_NOTARY_PROFILE`
  (profil créé avec `xcrun notarytool store-credentials`) : ils sont passés à `--signAppIdentity` et
  `--notaryProfile`, et Velopack signe et notarise.

## Premier lancement pour les utilisateurs

- Windows (SmartScreen) : si « Windows a protégé votre PC » s'affiche, cliquer sur « Informations
  complémentaires » puis « Exécuter quand même ».
- macOS (Gatekeeper) : ouvrir l'installeur, puis clic droit sur l'appli, « Ouvrir », et confirmer. Si le
  blocage persiste : Réglages Système, Confidentialité et sécurité, « Ouvrir quand même ». En dernier
  recours : `xattr -dr com.apple.quarantine /Applications/Missive.app`.

# Compiler et tester (WSL Ubuntu, utilisateur courant)

Les scripts de ce dossier ont été écrits pour un WSL (Ubuntu) : ils attendent les sources dans `~/watchdog`, `~/usbreset` et `~/OpenLinkHub`, et Go installé. Si le disque des sources n'est pas visible depuis WSL, les sources sont copiées vers WSL par `\\wsl.localhost\<distribution>\home\<utilisateur>\…` avant la compilation.

| Script | Rôle |
|---|---|
| `fmt-watchdog.sh` | `gofmt`, `go vet`, tests et compilation de la surveillance (sortie `~/watchdog/olh-watchdog`) |
| `build-watchdog.sh` | variante plus simple de la même chaîne |
| `run-preview.sh` | génère les trois mails d'exemple avec le vrai moteur (option `-preview`, sans IA) |
| `build-usbreset.sh` | compile `usbreset` et teste ses garde-fous |
| `build2.sh` | première compilation d'OpenLinkHub pour DSM (libusb statique, sans udev) |
| `build3.sh` | compilation d'OpenLinkHub avec `CGO_CFLAGS=-DNO_ICONV` (**version à utiliser**) |
| `olh-patches.sh` | extrait nos modifications d'OpenLinkHub sous forme de correctifs |

## OpenLinkHub : base et correctifs

- Dépôt amont : OpenLinkHub (GPL-3.0), commit de base **`949561e165df8f1d922ee4090e1c8b90910d4176`** (24/10/2026).
- `..\openlinkhub-patches\modifications-suivies.patch` : modifications de fichiers suivis (4 fichiers, 7 lignes ajoutées, 1 supprimée) : ouverture du Hub par chemin dans `src/devices/lsh/lsh.go`, et balises de compilation `noaudio` dans `src/audio/audio.c`, `audio.go`, `audio.h`.
- `..\openlinkhub-patches\nouveaux\src\audio\audio_stub.go` : module audio factice, actif avec la balise `noaudio` (PipeWire n'existe pas sur DSM).
- Option de compilation (pas du code) : `-DNO_ICONV`, dans `build3.sh`, pour que les noms et numéros de série USB soient lus dans un binaire glibc statique.

Pour reconstruire : dans un clone d'OpenLinkHub, `git checkout 949561e`, `git apply ..\openlinkhub-patches\modifications-suivies.patch`, copier `audio_stub.go` à sa place, puis lancer `build3.sh`.

**Licence** : OpenLinkHub est en GPL-3.0. Utiliser le binaire modifié chez soi n'impose rien ; si le binaire est partagé, le code source modifié (ces correctifs) doit l'être aussi. Ce dépôt est **local uniquement** : aucun serveur distant n'est configuré.

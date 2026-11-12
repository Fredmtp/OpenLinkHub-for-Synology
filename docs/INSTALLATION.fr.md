# Installation sur un Synology (DSM 7)
[English](INSTALLATION.md) · **Français**

> [!NOTE]
> L'application est en **français** ; ces guides existent aussi [en anglais](INSTALLATION.md).

> [!WARNING]
> Ces scripts s'exécutent en **root** sur votre NAS et peuvent **l'éteindre**. Ils contiennent des **valeurs d'exemple** (adresse `192.168.1.10`, compte `admin`, port SSH `22`, dossier `/volume1/openlinkhub`) : adaptez-les à votre installation avant de les lancer. Commencez toujours en **mode à blanc** (`MODE_SURVEILLANCE="-dry"` dans `start.sh`) et gardez une sauvegarde à jour.

Ce guide suppose que vous savez vous connecter en SSH au NAS (Panneau de configuration, Terminal et SNMP) et que le Hub iCUE LINK est branché en USB sur le NAS.

## 1. Compiler (sur un PC Linux ou WSL)

1. **OpenLinkHub pour DSM.** Cloner le projet source (<https://github.com/jurkovic-nikola/OpenLinkHub>), se placer sur le commit indiqué dans `outils-build/LISEZMOI.md`, appliquer `openlinkhub-patches/modifications-suivies.patch`, copier `openlinkhub-patches/nouveaux/src/audio/audio_stub.go`, puis lancer `outils-build/build3.sh` (binaire statique, balises `libusb noaudio osusergo netgo`, option `-DNO_ICONV`). `outils-build/build2.sh` prépare la bibliothèque USB statique.
2. **Reset USB du Hub.** `outils-build/build-usbreset.sh` compile `usbreset` (réinitialise le Hub, et lui seul, avant chaque démarrage d'OpenLinkHub).
3. **Surveillance.** `outils-build/build-watchdog.sh` (formatage, `go vet`, tests, compilation statique `GOOS=linux GOARCH=amd64`).

## 2. Copier sur le NAS

Créer `/volume1/openlinkhub/` et y copier : `OpenLinkHub`, `olh-watchdog`, `usbreset`, `start.sh`, `superviseur.sh`, `maintenance.sh`, `silence.sh`, `connus.txt`, ainsi que les dossiers `static/` et `web/` d'OpenLinkHub (les fichiers du thème de ce dépôt viennent par-dessus, étape 5). Rendre exécutables les binaires.

## 3. Secrets

Copier `dist/secrets.env.exemple` en `/volume1/openlinkhub/secrets.env` (droits 600, propriétaire root) et remplir ce qui vous sert : canal ntfy pour les notifications sur le téléphone, courriel (Microsoft Graph), `CONTROL_PASSWORD` pour la page de commandes. Tout est facultatif ; sans notification, les alertes ne se voient que sur la page d'état et dans le journal.

## 4. Tâches du Planificateur de DSM (compte root)

| Tâche | Déclencheur | Commande |
|---|---|---|
| Ventilation | Démarrage | `sh /volume1/openlinkhub/start.sh` |
| Superviseur | Toutes les minutes | `sh /volume1/openlinkhub/superviseur.sh` |

`start.sh` attend le Hub, le réinitialise, lance OpenLinkHub, puis la surveillance. Le superviseur relance l'un ou l'autre s'ils tombent (3 relances par heure au plus pour OpenLinkHub, 3 par 30 minutes pour la surveillance) et prévient.

Avant de passer en mode réel (la surveillance **éteint** alors le NAS) : essayez le mode à blanc plusieurs jours, vérifiez les seuils sur vos propres relevés (`outils-build/analyser-releves.py`), et faites un essai d'arrêt contrôlé.

## 5. Thème, tuiles et graphiques

Sur le PC : `bash outils-build/empaqueter-theme-goldcream.sh` construit `dist/theme-goldcream.tar.gz`. Copier l'archive et `dist/installer-theme-goldcream.sh` sur le NAS, puis `sudo sh installer-theme-goldcream.sh` : il sauvegarde les fichiers remplacés dans `/volume1/openlinkhub/theme-goldcream-precedent/`, installe le thème et relance OpenLinkHub seul. `retour-theme-goldcream.sh` remet l'apparence d'origine. Faire ensuite Ctrl+Maj+R dans le navigateur.

## 6. Prise Hue des ventilateurs (facultatif)

1. Sur le PC (PowerShell 7) : `dist/hue-lister.ps1` crée la clé d'accès (appui sur le bouton du pont) et liste vos appareils avec leur **nom exact** ; la clé reste dans votre profil, jamais affichée.
2. `dist/envoyer-hue.ps1` l'envoie dans `secrets.env` du NAS (adapter l'adresse et le compte SSH en tête du script).
3. Dans `start.sh`, l'option `-hue-plug "Nom de la prise"` active la surveillance de la prise (désactivée si le nom est vide ou la clé absente).
4. **Essayez** : éteignez la prise depuis l'appli Hue, la surveillance doit la rallumer en moins d'une minute et vous notifier. Ne provoquez pas à la main le « cycle de réparation ».

## 7. Contrôle depuis un second NAS (facultatif)

`dist/controle-ds1825.sh` (à renommer à votre goût) interroge la page d'état toutes les 5 minutes depuis un autre NAS ou une autre machine et alerte si elle ne répond plus. La plage des arrêts programmés de nuit se règle dans son `ntfy.env`.

## Vérifier

- `http://<adresse du NAS>:27003/` : l'interface, avec l'onglet « Températures NAS ».
- `http://<adresse du NAS>:27004/` : la page d'état de la surveillance (lecture seule).
- `watchdog.log` et `superviseur.log` dans `/volume1/openlinkhub/`.

# Historique des versions
[English](CHANGELOG.md) · **Français**

Dates au format jj/mm/2026. Les numéros de version ont été attribués après coup aux jalons ci-dessous (le suivi par version
commence avec la 1.0.0). Majeur = refonte ou changement de fonctionnement, mineur = nouvelle fonction ou protection, correctif = 1.0.x.

## 1.0.0 — 12/11 : thème, graphiques, prise des ventilateurs

- **Thème clair et sombre** sur toutes les pages (clair par défaut), bascule mémorisée par navigateur, polices locales, menu latéral sombre, tuiles du NAS avec point ou pastille d'état.
- Titre de l'application « OPENLINKHUB for SYNOLOGY WebUI », version affichée, historique.
- **Onglet « Températures NAS »** : graphiques sur une période choisie, seuils, alertes, bandes d'état (API `/api/releves`).
- **Prise connectée des ventilateurs** : rallumée si elle s'éteint, cycle de réparation si le Hub se fige, arrêt du NAS si rien n'y fait ; détection du Hub gelé (vitesses identiques 10 minutes).
- Surveillance : 262 tests.
- *Enseignements* : un Hub privé de courant laisse OpenLinkHub afficher les dernières valeurs (la ventilation « paraît » normale) ; une règle d'arrêt ne doit jamais s'appliquer après une simple réparation qui n'a pas pu relancer OpenLinkHub ; tester en réel les protections qui coupent du matériel.

## 0.8.0 — 08/11 : relance automatique

- Le superviseur relance **OpenLinkHub seul** (reset USB du Hub) quand son API ne répond plus depuis 3 minutes. Origine : un plantage au démarrage laissait 43 minutes sans lecture des sondes.
- Plage de silence du contrôle à distance selon le jour de la semaine (heures de redémarrage différentes).

## 0.7.0 — 02/11 : sonde figée, nettoyage RAID, baie USB

- Alerte « sonde figée » (valeur identique 4 heures) ; suivi du **nettoyage des données** du RAID ; surveillance de la **baie USB** (disparition, erreurs du noyau) ; contrôle de l'onduleur ; arrêt programmé de nuit.

## 0.6.0 — 29/10 au 01/11 : fiabilité des notifications et des noms de disques

- Notifications : seuls les canaux en échec sont retentés (fin des envois en triple) ; notification DSM abandonnée (le texte libre est refusé).
- Disques nommés d'après les baies DSM et non d'après `/dev/sataN`.

## 0.5.0 — 28/10 (soir) : mode réel

- Essai d'arrêt réel réussi, puis passage de la surveillance en mode réel (arrêt du NAS si la ventilation est perdue 15 minutes ou si un disque tient 45 °C pendant 1 minute).

## 0.4.0 — 28/10 : surveillance complète

- Ventilation en 4 états, seuils disques 40/43/45 °C, protection contre la boucle d'arrêts, alertes sonore et lumineuse, page de commandes protégée, mode maintenance, supervision (signe de vie, superviseur, contrôle depuis un second NAS), notifications ntfy et courriel.

## 0.3.0 — 27/10 : lecture des disques

- Compteurs SMART par baie, situations connues, premiers outils d'analyse des relevés.

## 0.2.0 — 26/10 : surveillance en Go

- Première surveillance (mode à blanc) et dépôt git.

## 0.1.0 — 24 et 25/10 : OpenLinkHub sur le NAS

- OpenLinkHub compilé pour DSM (libusb statique, sans PipeWire ni udev), lancé au démarrage, reset USB du Hub.

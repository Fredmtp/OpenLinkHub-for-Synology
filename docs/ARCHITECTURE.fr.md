# Architecture
[English](ARCHITECTURE.md) · **Français**

Trois programmes tournent sur le NAS, sous le compte root, dans `/volume1/openlinkhub/` :

| Programme | Rôle | Port |
|---|---|---|
| **OpenLinkHub** (compilé pour DSM) | Pilote le Hub iCUE LINK par USB : vitesses des ventilateurs, sondes du Commander DUO, éclairage. Interface web d'origine, thème or et crème par-dessus | 27003 |
| **`olh-watchdog`** (`watchdog/`, Go) | Boucle de sécurité (toutes les quelques secondes), collecte lente des capteurs, page d'état, API de relevés, notifications, commande de la prise Hue | 27004 |
| **`superviseur.sh`** | Lancé chaque minute par le Planificateur de DSM : relance la surveillance si elle est morte ou figée, relance OpenLinkHub si son API ne répond plus | — |

Le thème et les tuiles du tableau de bord sont de simples fichiers (`dist/static/`, `dist/web/`) lus par OpenLinkHub : **aucune modification du code Go d'OpenLinkHub** au-delà des quelques correctifs de compilation (`openlinkhub-patches/`).

## Ce que lit la surveillance

- **Depuis DSM** : températures des disques et compteurs SMART (par numéro de baie, pas par nom de périphérique, qui change à chaque démarrage), RAID (état, reconstruction, nettoyage des données), volume, carte mère et processeur, ventilateurs internes, onduleur, baie USB QNAP.
- **Depuis OpenLinkHub** : vitesse des ventilateurs du meuble, températures des deux sondes.
- **Depuis le pont Hue** (facultatif) : état de la prise qui alimente les ventilateurs.

## Principes de sécurité

1. **Plusieurs couches indépendantes.** Les températures des disques et de la carte mère ne dépendent ni d'OpenLinkHub ni du Hub : si tout le meuble tombe, ce garde-fou reste.
2. **Ne jamais croire une valeur qui ne bouge plus.** Un Hub sans courant laisse OpenLinkHub afficher les dernières valeurs : la surveillance détecte « toutes les vitesses identiques pendant 10 minutes ».
3. **Pas de boucle d'arrêts.** Après un arrêt automatique, les arrêts par la ventilation sont suspendus 6 heures (les températures restent surveillées).
4. **Mode à blanc** (`-dry`) et **mode maintenance** (fichier à échéance, 4 h au plus) pour travailler sans risque.
5. **Un seul bouton à la fois** : aucune action sur le matériel pendant la maintenance ou le délai de grâce du démarrage.

## Données

Les relevés (un fichier CSV par mois, une ligne par minute) sont dans `releves/`. L'API `/api/releves` les regroupe par intervalles (moyenne et maximum) pour les graphiques ; elle est en lecture seule.

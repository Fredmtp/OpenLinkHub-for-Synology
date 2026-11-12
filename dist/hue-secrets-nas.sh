#!/bin/sh
# Exécuté sur le NAS par envoyer-hue.ps1 (sudo sh): remplace les lignes HUE_* de secrets.env par celles de hue.env,
# puis supprime hue.env. N'affiche jamais les valeurs, seulement le nombre de lignes HUE_* présentes.
umask 077
DIR=/volume1/openlinkhub
SRC=/var/services/homes/admin/hue.env
[ -f "$SRC" ] || { echo "hue.env introuvable: rien à faire"; exit 1; }
[ -f "$DIR/secrets.env" ] || { echo "secrets.env introuvable dans $DIR"; exit 1; }
grep -v '^HUE_' "$DIR/secrets.env" > "$DIR/secrets.env.tmp"
cat "$SRC" >> "$DIR/secrets.env.tmp"
cat "$DIR/secrets.env.tmp" > "$DIR/secrets.env"   # écrit dans le fichier existant: propriétaire et droits conservés
rm -f "$DIR/secrets.env.tmp" "$SRC"
echo "Lignes HUE_BRIDGE: $(grep -c '^HUE_BRIDGE=' "$DIR/secrets.env"), HUE_KEY: $(grep -c '^HUE_KEY=' "$DIR/secrets.env") (1 et 1 attendus)"

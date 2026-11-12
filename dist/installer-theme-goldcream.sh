#!/bin/sh
# Installe le thème or et crème d'OpenLinkHub (clair par défaut, sombre au choix) à partir de theme-goldcream.tar.gz.
#   sudo sh installer-theme-goldcream.sh [chemin de l'archive]      (par défaut: dossier personnel de admin)
# Avant d'écraser quoi que ce soit, les fichiers existants sont copiés dans /volume1/openlinkhub/theme-goldcream-precedent/
# (même arborescence); `sh /volume1/openlinkhub/retour-theme-goldcream.sh` remet l'ancienne apparence.
# En fin d'installation, OpenLinkHub seul est relancé (`start.sh hub`, ~45 s, reset USB du Hub): il ne relit ses modèles
# de page (menu, en-tête) qu'au démarrage. La surveillance, la prise Hue et les ventilateurs ne sont pas touchés.
DIR=/volume1/openlinkhub
ARC="${1:-/var/services/homes/admin/theme-goldcream.tar.gz}"
SAV="$DIR/theme-goldcream-precedent"

[ -f "$ARC" ] || { echo "ERREUR: archive introuvable ($ARC)"; exit 1; }
[ -d "$DIR/web" ] && [ -d "$DIR/static" ] || { echo "ERREUR: $DIR ne contient pas web/ et static/"; exit 1; }

mkdir -p "$SAV" || exit 1
tar tzf "$ARC" | grep -v '/$' > "$SAV/liste.txt" || { echo "ERREUR: archive illisible"; exit 1; }
n=0
while read -r f; do
  if [ -f "$DIR/$f" ]; then
    mkdir -p "$SAV/$(dirname "$f")" && cp -p "$DIR/$f" "$SAV/$f" && n=$((n + 1))
  fi
done < "$SAV/liste.txt"
echo "$n fichier(s) existant(s) sauvegardé(s) dans $SAV"

tar xzf "$ARC" -C "$DIR" || { echo "ERREUR: extraction impossible"; exit 1; }
echo "$(wc -l < "$SAV/liste.txt" | tr -d ' ') fichier(s) du thème or et crème installé(s)"
rm -f "$ARC"

echo "Relance d'OpenLinkHub seul (environ 45 s)..."
sh "$DIR/start.sh" hub
echo "Terminé. Rechargez la page d'OpenLinkHub avec Ctrl+F5."

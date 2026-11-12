#!/bin/sh
# Remet l'apparence d'origine d'OpenLinkHub (avant le thème or et crème): restaure les fichiers sauvegardés par installer-theme-goldcream.sh
# et supprime ceux que le thème avait ajoutés. Puis relance OpenLinkHub seul.
#   sudo sh /volume1/openlinkhub/retour-theme-goldcream.sh
DIR=/volume1/openlinkhub
SAV="$DIR/theme-goldcream-precedent"
[ -f "$SAV/liste.txt" ] || { echo "ERREUR: pas de sauvegarde ($SAV/liste.txt)"; exit 1; }
while read -r f; do
  [ "$f" = "retour-theme-goldcream.sh" ] && continue
  if [ -f "$SAV/$f" ]; then cp -p "$SAV/$f" "$DIR/$f"; else rm -f "$DIR/$f"; fi
done < "$SAV/liste.txt"
echo "Apparence d'origine restaurée."
sh "$DIR/start.sh" hub
echo "Terminé. Rechargez la page d'OpenLinkHub avec Ctrl+F5."

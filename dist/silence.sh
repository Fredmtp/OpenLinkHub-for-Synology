#!/bin/sh
# Coupe l'alerte SONORE du NAS (bips) pendant un moment, sans toucher aux alertes ni aux arrêts.
#
#   sudo sh silence.sh on [minutes]   silence pour N minutes (30 par défaut, 240 au plus)
#   sudo sh silence.sh off            rétablit le son tout de suite
#   sh silence.sh status              indique si le silence est actif
#
# Seul le buzzer est concerné: les notifications (ntfy, mail), la page d'état et les arrêts
# automatiques restent actifs. Le silence se termine de lui-même à l'heure indiquée: un oubli est
# sans conséquence. La surveillance relit le fichier chaque seconde.

FICHIER=/volume1/openlinkhub/silence
MAX=240

case "$1" in
  on)
    min="${2:-30}"
    case "$min" in ''|*[!0-9]*) echo "Durée invalide: indiquez un nombre de minutes."; exit 1;; esac
    [ "$min" -gt "$MAX" ] && min=$MAX
    [ "$min" -lt 1 ] && min=1
    fin=$(( $(date +%s) + min * 60 ))
    echo "$fin" > "$FICHIER" || { echo "Écriture impossible (lancez avec sudo)."; exit 1; }
    echo "Alerte sonore coupée pour $min minute(s), jusqu'à environ $(date -d "@$fin" '+%H:%M' 2>/dev/null || echo "+${min} min")."
    echo "Les notifications et les arrêts automatiques restent actifs. Fin anticipée: sudo sh silence.sh off"
    ;;
  off)
    rm -f "$FICHIER" && echo "Silence retiré: l'alerte sonore est rétablie."
    ;;
  status|"")
    if [ -f "$FICHIER" ]; then
      fin=$(cat "$FICHIER" 2>/dev/null)
      reste=$(( fin - $(date +%s) ))
      if [ "$reste" -gt 0 ] 2>/dev/null; then
        echo "Silence ACTIF, encore $(( reste / 60 )) minute(s)."
      else
        echo "Fichier présent mais périmé: silence inactif."
      fi
    else
      echo "Silence inactif: l'alerte sonore est active."
    fi
    ;;
  *)
    echo "Usage: sudo sh silence.sh on [minutes] | off | status"
    exit 1
    ;;
esac

#!/bin/sh
# Met en veille l'arrêt automatique « ventilation » du NAS pendant une intervention (NAS allumé).
#
#   sudo sh maintenance.sh on [minutes]   veille pour N minutes (30 par défaut, 240 au plus)
#   sudo sh maintenance.sh off            rétablit l'arrêt ventilation tout de suite
#   sh maintenance.sh status              indique si la veille est active
#
# Pendant la veille, seule la règle « ventilateurs » n'éteint plus le NAS (et ses alertes sont
# tues). Les seuils de température (sondes du meuble, processeur, disques, carte mère) restent
# actifs. La veille se termine d'elle-même à l'heure indiquée: un oubli est sans conséquence.
# La surveillance relit le fichier toutes les 10 s et envoie une notification au début et à la fin.

FICHIER=/volume1/openlinkhub/maintenance
MAX=240

case "$1" in
  on)
    min="${2:-30}"
    case "$min" in ''|*[!0-9]*) echo "Durée invalide: indiquez un nombre de minutes."; exit 1;; esac
    [ "$min" -gt "$MAX" ] && min=$MAX
    [ "$min" -lt 1 ] && min=1
    fin=$(( $(date +%s) + min * 60 ))
    echo "$fin" > "$FICHIER" || { echo "Écriture impossible (lancez avec sudo)."; exit 1; }
    echo "Veille de l'arrêt ventilation pour $min minute(s), jusqu'à environ $(date -d "@$fin" '+%H:%M' 2>/dev/null || echo "+${min} min")."
    echo "Les seuils de température restent actifs. Fin anticipée: sudo sh maintenance.sh off"
    ;;
  off)
    rm -f "$FICHIER" && echo "Veille retirée: l'arrêt ventilation est rétabli."
    ;;
  status|"")
    if [ -f "$FICHIER" ]; then
      fin=$(cat "$FICHIER" 2>/dev/null)
      reste=$(( fin - $(date +%s) ))
      if [ "$reste" -gt 0 ] 2>/dev/null; then
        echo "Veille ACTIVE, encore $(( reste / 60 )) minute(s)."
      else
        echo "Fichier présent mais périmé: veille inactive."
      fi
    else
      echo "Veille inactive: l'arrêt ventilation est actif."
    fi
    ;;
  *)
    echo "Usage: sudo sh maintenance.sh on [minutes] | off | status"
    exit 1
    ;;
esac

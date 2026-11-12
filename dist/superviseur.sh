#!/bin/sh
# Superviseur de la surveillance du meuble (décision du 28/10/2026).
# À lancer CHAQUE MINUTE par le Planificateur de tâches de DSM (utilisateur root):
#   sh /volume1/openlinkhub/superviseur.sh
#
# Il vérifie que la surveillance (olh-watchdog) est vivante: le processus existe ET son signe de vie
# (/tmp/olh-watchdog.beat, réécrit toutes les 20 s) date de moins de 2 minutes. Sinon il la relance
# (surveillance seule: `start.sh surveillance`, sans toucher à OpenLinkHub ni au Hub) et envoie une
# alerte ntfy. Il vérifie aussi que l'API d'OpenLinkHub répond (voir verifier_hub plus bas). Au plus 3 relances en 30 minutes: ensuite alerte seule (plantage répété), une fois par
# 30 minutes. Quand tout va de nouveau bien, un message de rétablissement part.
#
# Il NE FAIT RIEN pendant:
#   - la veille de maintenance (maintenance.sh on): indispensable pour lancer la surveillance à la
#     main (essais) sans qu'il la relance en double;
#   - un arrêt du NAS demandé par la surveillance (fichier .stop récent);
#   - l'exécution de start.sh (démarrage ou relance en cours).
#
# Les secrets ntfy sont lus dans secrets.env; le jeton n'est jamais passé en ligne de commande.

DIR="${OLH_DIR:-/volume1/openlinkhub}"
BEAT="${OLH_BEAT:-/tmp/olh-watchdog.beat}"
PROC="${OLH_PROC:-/proc}"
MAXAGE="${OLH_MAXAGE:-120}"
MAXRELANCES="${OLH_MAXRELANCES:-3}"
FENETRE="${OLH_FENETRE:-1800}"
# OpenLinkHub (API REST): 0 relance = contrôle désactivé
HUB_URL="${OLH_HUB_URL:-http://192.168.1.10:27003/api/devices/}"
HUB_DELAI="${OLH_HUB_DELAI:-180}"
HUB_MAX="${OLH_HUB_MAX:-3}"
HUB_FENETRE="${OLH_HUB_FENETRE:-3600}"
HUB_DEPUIS="$DIR/superviseur-hub-depuis"
HUB_VU="$DIR/superviseur-hub-vu"
HUB_PROBLEME="$DIR/superviseur-hub-probleme"
HUB_RELANCES="$DIR/superviseur-hub-relances"
HUB_DERNIERE="$DIR/superviseur-hub-alerte"

LOG="$DIR/superviseur.log"
RELANCES="$DIR/superviseur-relances"
PROBLEME="$DIR/superviseur-probleme"
DERNIERE="$DIR/superviseur-alerte"
VERROU="$DIR/.superviseur.verrou"

maintenant() { date +%s; }
log() { echo "$(date '+%Y/%m/%d %H:%M:%S') $*" >> "$LOG"; }
mtime() { stat -c %Y "$1" 2>/dev/null || date -r "$1" +%s 2>/dev/null; }

secret() {
  [ -f "$DIR/secrets.env" ] || return 0
  grep "^$1[ ]*=" "$DIR/secrets.env" | head -1 | tr -d '\r' | sed 's/^[^=]*=[ ]*//; s/^"\(.*\)"$/\1/; s/^'"'"'\(.*\)'"'"'$/\1/'
}

# alerter TITRE(ASCII) TEXTE PRIORITE ETIQUETTES
alerter() {
  topic=$(secret NTFY_TOPIC)
  [ -n "$topic" ] || { log "pas de NTFY_TOPIC: alerte non envoyee ($1)"; return 0; }
  serveur=$(secret NTFY_SERVER)
  [ -n "$serveur" ] || serveur=https://ntfy.sh
  jeton=$(secret NTFY_TOKEN)
  if [ -n "$jeton" ]; then
    printf 'header = "Authorization: Bearer %s"\n' "$jeton" | \
      curl -s -m 15 -o /dev/null --config - -H "Title: $1" -H "Priority: $3" -H "Tags: $4" -d "$2" "$serveur/$topic"
  else
    curl -s -m 15 -o /dev/null -H "Title: $1" -H "Priority: $3" -H "Tags: $4" -d "$2" "$serveur/$topic"
  fi || log "alerte non envoyee: $1"
}

now=$(maintenant)

# un seul superviseur à la fois (verrou périmé après 5 minutes)
if ! mkdir "$VERROU" 2>/dev/null; then
  v=$(mtime "$VERROU")
  if [ -n "$v" ] && [ $((now - v)) -gt 300 ]; then
    rmdir "$VERROU" 2>/dev/null
    mkdir "$VERROU" 2>/dev/null || exit 0
  else
    exit 0
  fi
fi
trap 'rmdir "$VERROU" 2>/dev/null' EXIT

# veille de maintenance (même règle que la surveillance: date de fin, 4 h au plus après l'écriture)
if [ -f "$DIR/maintenance" ]; then
  fin=$(tr -d ' \r\n' < "$DIR/maintenance" 2>/dev/null)
  case "$fin" in
    ''|*[!0-9]*) ;;
    *)
      m=$(mtime "$DIR/maintenance")
      [ -n "$m" ] && [ "$fin" -gt $((m + 14400)) ] && fin=$((m + 14400))
      [ "$fin" -gt "$now" ] && exit 0
      ;;
  esac
fi

# arrêt du NAS en cours (le fichier .stop n'est cru que 15 minutes: un arrêt raté ne doit pas
# désactiver la supervision pour toujours)
if [ -f "$BEAT.stop" ]; then
  s=$(mtime "$BEAT.stop")
  [ -n "$s" ] && [ $((now - s)) -lt 900 ] && exit 0
fi

# start.sh en cours d'exécution (démarrage, relance manuelle)
for f in "$PROC"/[0-9]*/cmdline; do
  [ -r "$f" ] || continue
  if tr '\0' ' ' < "$f" 2>/dev/null | grep -q 'start\.sh'; then exit 0; fi
done

# ---- 1. la surveillance (olh-watchdog) est-elle vivante? ----
verifier_surveillance() {
vivant=0
for f in "$PROC"/[0-9]*/comm; do
  [ "$(cat "$f" 2>/dev/null)" = "olh-watchdog" ] && { vivant=1; break; }
done
age=""
if [ -f "$BEAT" ]; then
  b=$(mtime "$BEAT")
  [ -n "$b" ] && age=$((now - b))
fi

etat=ok
detail=""
if [ "$vivant" = 0 ]; then
  etat=arretee
  detail="le processus olh-watchdog n'existe plus"
elif [ -z "$age" ]; then
  etat=figee
  detail="aucun signe de vie"
elif [ "$age" -gt "$MAXAGE" ]; then
  etat=figee
  detail="aucun signe de vie depuis $age s"
fi

if [ "$etat" = ok ]; then
  if [ -f "$PROBLEME" ]; then
    depuis=$(cat "$PROBLEME")
    rm -f "$PROBLEME" "$RELANCES" "$DERNIERE"
    log "surveillance retablie"
    alerter "Surveillance du meuble retablie" "La surveillance du NAS tourne de nouveau (signe de vie reçu). Problème détecté depuis $(date -d "@$depuis" '+%d/%m à %H:%M' 2>/dev/null || echo "$depuis")." 3 white_check_mark
  fi
  return 0
fi

# --- problème ---
[ -f "$PROBLEME" ] || echo "$now" > "$PROBLEME"

# relances des 30 dernières minutes
if [ -f "$RELANCES" ]; then
  : > "$RELANCES.tmp"
  while read -r t; do
    [ -n "$t" ] && [ $((now - t)) -lt "$FENETRE" ] && echo "$t" >> "$RELANCES.tmp"
  done < "$RELANCES"
  mv "$RELANCES.tmp" "$RELANCES"
fi
n=0
[ -f "$RELANCES" ] && n=$(wc -l < "$RELANCES" | tr -d ' ')

if [ "$n" -lt "$MAXRELANCES" ]; then
  echo "$now" >> "$RELANCES"
  log "surveillance $etat ($detail): relance $((n + 1))/$MAXRELANCES"
  sh "$DIR/start.sh" surveillance >> "$LOG" 2>&1
  alerter "Surveillance du meuble $etat, relancee" "La surveillance du NAS était ${etat} ($detail). Relance automatique $((n + 1))/$MAXRELANCES. Vérifiez la page d'état et watchdog.log." 5 warning
else
  d=0
  [ -f "$DERNIERE" ] && d=$(cat "$DERNIERE")
  log "surveillance $etat ($detail): $n relances en 30 min, pas de nouvelle relance"
  if [ $((now - d)) -ge "$FENETRE" ]; then
    echo "$now" > "$DERNIERE"
    alerter "Surveillance du meuble HORS SERVICE" "La surveillance du NAS ne tient pas: $n relances en 30 minutes sans succès ($detail). Plus de relance automatique. Le NAS n'est PLUS protégé: intervenez (sudo sh /volume1/openlinkhub/start.sh)." 5 rotating_light
  fi
fi
}

# ---- 2. OpenLinkHub répond-il? ----
# Si l'API (port 27003) ne répond plus pendant HUB_DELAI secondes (plantage au démarrage, processus bloqué),
# on relance OpenLinkHub seul (`start.sh hub`: reset USB du Hub + OpenLinkHub; la surveillance n'est pas touchée).
# Au plus HUB_MAX relances par HUB_FENETRE (1 h), ensuite alerte seule, une fois par fenêtre.
# Cas vécu le 08/11/2026: panic au démarrage (réponse du Hub tronquée), 43 minutes sans lecture des sondes.
verifier_hub() {
  [ "$HUB_MAX" -gt 0 ] || return 0
  code=$(curl -s -m 5 -o /dev/null -w '%{http_code}' "$HUB_URL" 2>/dev/null)
  if [ "$code" = 200 ]; then
    if [ -f "$HUB_PROBLEME" ]; then
      depuis=$(cat "$HUB_PROBLEME")
      rm -f "$HUB_PROBLEME" "$HUB_DEPUIS" "$HUB_RELANCES" "$HUB_DERNIERE"
      log "OpenLinkHub retabli"
      alerter "OpenLinkHub retabli" "OpenLinkHub répond de nouveau (sondes et ventilateurs lisibles). Problème détecté depuis $(date -d "@$depuis" '+%d/%m à %H:%M' 2>/dev/null || echo "$depuis")." 3 white_check_mark
    fi
    rm -f "$HUB_DEPUIS" "$HUB_VU"
    return 0
  fi
  # le décompte repart à zéro si le superviseur n'a pas regardé pendant plus de 150 s
  # (maintenance, démarrage ou relance en cours, NAS rallumé): pas de relance sur un vieux constat
  vu=0
  [ -f "$HUB_VU" ] && vu=$(cat "$HUB_VU")
  echo "$now" > "$HUB_VU"
  if [ ! -f "$HUB_DEPUIS" ] || [ $((now - vu)) -gt 150 ]; then echo "$now" > "$HUB_DEPUIS"; fi
  d0=$(cat "$HUB_DEPUIS")
  [ $((now - d0)) -ge "$HUB_DELAI" ] || return 0
  [ -f "$HUB_PROBLEME" ] || echo "$d0" > "$HUB_PROBLEME"

  if [ -f "$HUB_RELANCES" ]; then
    : > "$HUB_RELANCES.tmp"
    while read -r t; do
      [ -n "$t" ] && [ $((now - t)) -lt "$HUB_FENETRE" ] && echo "$t" >> "$HUB_RELANCES.tmp"
    done < "$HUB_RELANCES"
    mv "$HUB_RELANCES.tmp" "$HUB_RELANCES"
  fi
  n=0
  [ -f "$HUB_RELANCES" ] && n=$(wc -l < "$HUB_RELANCES" | tr -d ' ')

  if [ "$n" -lt "$HUB_MAX" ]; then
    echo "$now" >> "$HUB_RELANCES"
    echo "$now" > "$HUB_DEPUIS"   # prochaine relance au plus tôt dans HUB_DELAI secondes
    log "OpenLinkHub ne repond plus (code '$code') depuis $((now - d0)) s: relance $((n + 1))/$HUB_MAX"
    alerter "OpenLinkHub ne repond plus, relance" "OpenLinkHub ne répond plus depuis $((now - d0)) s (sondes et ventilateurs non lisibles). Relance automatique $((n + 1))/$HUB_MAX (reset du Hub). La surveillance reste active." 5 warning
    sh "$DIR/start.sh" hub >> "$LOG" 2>&1
  else
    d=0
    [ -f "$HUB_DERNIERE" ] && d=$(cat "$HUB_DERNIERE")
    log "OpenLinkHub ne repond plus: $n relances en $((HUB_FENETRE / 60)) min, pas de nouvelle relance"
    if [ $((now - d)) -ge "$HUB_FENETRE" ]; then
      echo "$now" > "$HUB_DERNIERE"
      alerter "OpenLinkHub HORS SERVICE" "OpenLinkHub ne repart pas: $n relances en $((HUB_FENETRE / 60)) minutes sans succès. Plus de relance automatique. Les ventilateurs du meuble gardent leurs réglages mais les sondes ne sont plus lues: intervenez (sudo sh /volume1/openlinkhub/start.sh, puis collez la fin de openlinkhub-run.log)." 5 rotating_light
    fi
  fi
}

verifier_surveillance
verifier_hub
exit 0

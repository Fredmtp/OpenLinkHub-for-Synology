#!/bin/sh
# À installer sur le DS1522+ (pas sur le DS1825+): vérifie depuis l'extérieur que la surveillance du
# DS1825+ répond, par la page d'état (port 27004, servie par la surveillance elle-même). Couvre ce que le
# superviseur local ne peut pas voir: NAS figé ou éteint, coupure de courant, réseau ou VPN coupé.
#
# Planificateur de tâches du DS1522+ (compte root), toutes les 5 minutes:
#   sh /volume1/<dossier>/controle-ds1825.sh
# Réglages: un fichier ntfy.env dans le même dossier (NTFY_TOPIC=..., et si besoin NTFY_SERVER=...,
# NTFY_TOKEN=...), les mêmes valeurs que secrets.env du DS1825+. Le jeton n'est jamais passé en ligne
# de commande.
#
# Alerte après 3 échecs consécutifs (15 minutes), rappel toutes les 6 heures, message de rétablissement
# avec la durée. Pour une absence prévue (coupure voulue du DS1825+), créez le fichier `pause` dans ce
# dossier (touch pause) puis supprimez-le (rm pause) quand c'est fini.

DIR="$(cd "$(dirname "$0")" && pwd)"
URL="${CTRL_URL:-http://192.168.1.10:27004/}"
ECHECS_MAX="${CTRL_ECHECS:-3}"
RAPPEL="${CTRL_RAPPEL:-21600}"
ETAT="$DIR/controle-etat"        # lignes: échecs consécutifs, début de la panne, dernière alerte
LOG="$DIR/controle.log"

log() { echo "$(date '+%Y/%m/%d %H:%M:%S') $*" >> "$LOG"; }

secret() {
  [ -f "$DIR/ntfy.env" ] || return 0
  grep "^$1[ ]*=" "$DIR/ntfy.env" | head -1 | tr -d '\r' | sed 's/^[^=]*=[ ]*//; s/^"\(.*\)"$/\1/; s/^'"'"'\(.*\)'"'"'$/\1/'
}

alerter() {   # TITRE(ASCII) TEXTE PRIORITE ETIQUETTES
  topic=$(secret NTFY_TOPIC)
  [ -n "$topic" ] || { log "pas de NTFY_TOPIC dans ntfy.env: alerte non envoyee ($1)"; return 0; }
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

now=$(date +%s)

# Mode essai: sh controle-ds1825.sh test
# Affiche le code de réponse de la page d'état du DS1825+ et envoie un message ntfy de test. Ne touche
# ni à l'état des échecs ni à la pause.
if [ "$1" = test ]; then
  code=$(curl -s -m 15 -o /dev/null -w '%{http_code}' "$URL" 2>/dev/null)
  echo "Page d'état du DS1825+ ($URL): code HTTP ${code:-aucun} (200 = joignable)"
  if [ -f "$DIR/ntfy.env" ] && [ -n "$(secret NTFY_TOPIC)" ]; then
    alerter "TEST controle du DS1825+" "Message de test du contrôle depuis le DS1522+. Page d'état: code ${code:-aucun}." 3 white_check_mark
    echo "Message de test envoyé: regardez ntfy sur le téléphone."
  else
    echo "ntfy.env introuvable ou sans NTFY_TOPIC dans $DIR: aucun message envoyé."
  fi
  exit 0
fi

[ -f "$DIR/pause" ] && exit 0

# Arrêt programmé de nuit du DS1825+ (DSM: minuit → démarrage): pendant cette plage, pas de contrôle, donc pas
# d'alerte « ne répond plus » ni de rétablissement chaque nuit. Début 23:55. Fin selon le jour où le NAS
# redémarre (horaires DSM + 20 minutes pour le NAS, le Hub et la surveillance):
#   mardi à vendredi: démarrage 6 h 50  → fin 07:10   (CTRL_ARRET_A)
#   lundi:            démarrage 5 h 00  → fin 05:20   (CTRL_ARRET_A_LUN)
#   samedi, dimanche: démarrage 9 h 30  → fin 09:50   (CTRL_ARRET_A_WE)
# Réglable dans ntfy.env (CTRL_ARRET_DE=HH:MM et les trois fins ci-dessus), « off » dans CTRL_ARRET_DE pour
# désactiver la plage. CTRL_HEURE=HHMM et CTRL_JOUR=1..7 (1 = lundi) servent aux essais.
hhmm() { echo "$1" | tr -d ':' | sed 's/^0*//; s/^$/0/'; }
arret_de=$(secret CTRL_ARRET_DE); [ -n "$arret_de" ] || arret_de="${CTRL_ARRET_DE:-23:55}"
jour="${CTRL_JOUR:-$(date +%u)}"
case "$jour" in
  1) cle=CTRL_ARRET_A_LUN; defaut=05:20 ;;
  6|7) cle=CTRL_ARRET_A_WE; defaut=09:50 ;;
  *) cle=CTRL_ARRET_A; defaut=07:10 ;;
esac
arret_a=$(secret "$cle")
[ -n "$arret_a" ] || { eval "arret_a=\${$cle:-}"; }
[ -n "$arret_a" ] || arret_a="$defaut"
if [ "$arret_de" != off ]; then
  h=$(hhmm "${CTRL_HEURE:-$(date +%H%M)}"); de=$(hhmm "$arret_de"); a=$(hhmm "$arret_a")
  if [ "$de" -gt "$a" ]; then
    if [ "$h" -ge "$de" ] || [ "$h" -lt "$a" ]; then exit 0; fi
  else
    if [ "$h" -ge "$de" ] && [ "$h" -lt "$a" ]; then exit 0; fi
  fi
fi

code=$(curl -s -m 15 -o /dev/null -w '%{http_code}' "$URL" 2>/dev/null)

echecs=0; debut=0; derniere=0
if [ -f "$ETAT" ]; then
  echecs=$(sed -n 1p "$ETAT"); debut=$(sed -n 2p "$ETAT"); derniere=$(sed -n 3p "$ETAT")
fi
case "$echecs" in ''|*[!0-9]*) echecs=0 ;; esac
case "$debut" in ''|*[!0-9]*) debut=0 ;; esac
case "$derniere" in ''|*[!0-9]*) derniere=0 ;; esac

if [ "$code" = "200" ]; then
  if [ "$echecs" -ge "$ECHECS_MAX" ]; then
    duree=$(( (now - debut) / 60 ))
    log "DS1825+ de nouveau joignable apres ${duree} min"
    alerter "Surveillance du DS1825+ retablie" "La page d'état du DS1825+ répond de nouveau (panne d'environ ${duree} minutes)." 3 white_check_mark
  fi
  rm -f "$ETAT"
  exit 0
fi

# échec
[ "$echecs" -eq 0 ] && debut=$now
echecs=$((echecs + 1))
log "echec $echecs (code '${code:-aucun}')"
if [ "$echecs" -ge "$ECHECS_MAX" ] && [ $((now - derniere)) -ge "$RAPPEL" ]; then
  derniere=$now
  alerter "DS1825+ ne repond plus" "La page d'état de la surveillance du DS1825+ ne répond plus depuis environ $(( (now - debut) / 60 )) minutes (code ${code:-aucun}). Le NAS est peut-être éteint, figé, ou le VPN est coupé. Si l'arrêt est voulu, créez le fichier pause à côté de ce script." 5 rotating_light
fi
printf '%s\n%s\n%s\n' "$echecs" "$debut" "$derniere" > "$ETAT"
exit 0

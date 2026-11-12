#!/bin/sh
# Démarrage de la ventilation du meuble: OpenLinkHub puis la surveillance.
# Lancé par le Planificateur de tâches de DSM (tâche déclenchée "Démarrage", utilisateur root).
#
#   sudo sh start.sh                 tout relancer: OpenLinkHub (avec reset du Hub) puis la surveillance
#   sudo sh start.sh surveillance    relancer la surveillance seule (utilisé par superviseur.sh)
#   sudo sh start.sh hub             relancer OpenLinkHub seul, avec reset du Hub (utilisé par superviseur.sh
#                                    quand l'API d'OpenLinkHub ne répond plus depuis 3 min)
#
# MODE_SURVEILLANCE: "-dry" = journalise seulement, n'éteint rien (phase de test).
#                    "" (vide) = la surveillance éteint réellement le NAS.
MODE_SURVEILLANCE=""

DIR=/volume1/openlinkhub
HUB_USB=/sys/bus/usb/devices/3-1   # iCUE LINK System Hub (port USB du NAS)
LOG="$DIR/start.log"
BEAT=/tmp/olh-watchdog.beat        # signe de vie de la surveillance, lu par superviseur.sh

cd "$DIR" || exit 1
say() { echo "$(date '+%Y/%m/%d %H:%M:%S') $*" >> "$LOG"; }

# Arrêt d'un processus suivi par son fichier PID: $1 = fichier PID, $2 = signal.
# OpenLinkHub s'arrête proprement sur INT. La surveillance, lancée en arrière-plan,
# ignore INT (hérité du shell): elle s'arrête sur TERM.
stop_pid() {
  [ -f "$1" ] || return 0
  pid=$(cat "$1")
  kill -"$2" "$pid" 2>/dev/null || return 0
  i=0
  while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 15 ]; do sleep 1; i=$((i + 1)); done
  kill -0 "$pid" 2>/dev/null && { say "arrêt forcé du processus $pid"; kill -KILL "$pid" 2>/dev/null; }
  rm -f "$1"
}

# -secrets: notifications (ntfy, mail); absent = désactivées. -listen: page d'état (réseau local).
# -buzzer: alerte sonore par le buzzer du NAS (5 bips en boucle, bip isolé si ventilation partielle);
#          -silence: fichier écrit par silence.sh. -record: relevé des températures (un CSV par mois,
#          une ligne par minute) dans $DIR/releves. -autostop: marqueur du dernier arrêt automatique pour
#          perte de ventilation (6 h sans nouvel arrêt par la ventilation si la cause persiste).
#          -leds: alerte lumineuse sur les ventilateurs (profil « circle »): un ventilateur en panne seul,
#          les 4 si la ventilation est dégradée ou perdue, le ventilateur de la zone d'un disque trop chaud,
#          les 3 extracteurs en alternance (3 s) si le NAS chauffe; -led-state: profils d'origine à remettre.
#          -qnap-vendor QNAP: surveille la baie USB QNAP TR-004 (archives et sauvegarde): alerte si elle disparaît
#          de /proc/scsi/scsi ou si le journal du noyau signale des erreurs USB sur elle (pas de SMART possible).
#          (-dsm-notify RETIRÉ le 29/10/2026: synodsmnotify n'accepte que des clés de texte DSM, jamais
#          du texte libre; chaque envoi échouait et faisait répéter ntfy et le mail 3 fois.)
#          -hue-plug: nom de la prise Hue qui alimente les ventilateurs (HUE_BRIDGE et HUE_KEY dans secrets.env, envoyés par
#          envoyer-hue.ps1): rallumée si elle s'éteint, coupée 15 s puis rallumée si le Hub se bloque (hueguard.go);
#          -hub-restart-cmd relance alors OpenLinkHub seul.
#          -heartbeat: signe de vie réécrit toutes les 20 s, surveillé par superviseur.sh.
#          -restart-cmd: commande du bouton « Relancer la surveillance » de la page de commandes
#          (http://192.168.1.10:27004/commandes, mot de passe CONTROL_PASSWORD dans secrets.env).
#          Pendant la maintenance, les 4 ventilateurs prennent le profil de lumière « maintenance »
#          (-led-maintenance static: couleur fixe, réglable dans OpenLinkHub; les profils de lumière portent
#          le nom d'un mode et ne se créent pas) et un rappel part toutes les 30 minutes.
lancer_surveillance() {
  rm -f "$BEAT" "$BEAT.stop"
  nohup ./olh-watchdog $MODE_SURVEILLANCE -url http://192.168.1.10:27003 \
    -secrets "$DIR/secrets.env" -listen 192.168.1.10:27004 \
    -state "$DIR/watchdog-state.json" -maintenance "$DIR/maintenance" -known "$DIR/connus.txt" -events "$DIR/evenements.jsonl" \
    -buzzer /dev/ttyS1 -silence "$DIR/silence" -record "$DIR/releves" -autostop "$DIR/arret-auto.json" \
    -leds -led-state "$DIR/leds-etat.json" -heartbeat "$BEAT" \
    -restart-cmd "sh $DIR/start.sh surveillance" -led-maintenance static -qnap-vendor QNAP \
    -hue-plug "Aération NAS" -hub-restart-cmd "sh $DIR/start.sh hub" \
    -log "$DIR/watchdog.log" >> "$DIR/watchdog-run.log" 2>&1 &
  echo $! > "$DIR/olh-watchdog.pid"
}

if [ "$1" = "surveillance" ]; then
  say "== relance de la surveillance seule (surveillance: '${MODE_SURVEILLANCE:-réelle}')"
  stop_pid "$DIR/olh-watchdog.pid" TERM
  pkill -TERM -x olh-watchdog 2>/dev/null
  sleep 3
  lancer_surveillance
  say "surveillance relancée"
  exit 0
fi

# Arrête OpenLinkHub (jamais la surveillance), remet le Hub à zéro sur le bus USB puis relance OpenLinkHub.
redemarrer_olh() {
  stop_pid "$DIR/openlinkhub.pid" INT
  # exemplaires lancés à la main ou par une ancienne version du script
  pkill -INT -x OpenLinkHub 2>/dev/null
  sleep 5
  pkill -KILL -x OpenLinkHub 2>/dev/null   # un OpenLinkHub bloqué qui ignore INT

  # attend que le Hub soit présent sur le bus USB (jusqu'à 60 s, utile au démarrage du NAS)
  i=0
  while [ ! -d "$HUB_USB" ] && [ "$i" -lt 60 ]; do sleep 1; i=$((i + 1)); done

  if [ ! -d "$HUB_USB" ]; then
    say "ATTENTION: Hub introuvable sur $HUB_USB"
  elif [ -x "$DIR/usbreset" ]; then
    # vrai reset USB (USBDEVFS_RESET): sans lui, le Hub ne répond plus à une 2e ouverture
    # n'agit que si le port désigne bien le Hub (1b1c:0c3f), jamais sur le QNAP ou l'onduleur
    say "$("$DIR/usbreset" "$HUB_USB" 1b1c:0c3f 2>&1)"
    sleep 5
  fi

  nohup ./OpenLinkHub >> "$DIR/openlinkhub-run.log" 2>&1 &
  echo $! > "$DIR/openlinkhub.pid"

  # laisse à OpenLinkHub le temps de détecter le Hub et ses ventilateurs
  sleep 30
}

# OpenLinkHub seul (utilisé par superviseur.sh quand son API ne répond plus): la surveillance n'est pas touchée
if [ "$1" = "hub" ]; then
  say "== relance d'OpenLinkHub seul (reset du Hub)"
  redemarrer_olh
  say "OpenLinkHub relancé"
  exit 0
fi

say "== démarrage (surveillance: '${MODE_SURVEILLANCE:-réelle}')"

stop_pid "$DIR/olh-watchdog.pid" TERM
pkill -TERM -x olh-watchdog 2>/dev/null
redemarrer_olh

lancer_surveillance
say "OpenLinkHub et surveillance lancés"

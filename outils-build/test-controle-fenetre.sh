#!/bin/sh
# Essai de la plage d'arrêt de nuit de dist/controle-ds1825.sh, avec un faux curl (page d'état injoignable).
# À lancer sous WSL: sh outils-build/test-controle-fenetre.sh   (de préférence avec dash pour le POSIX)

SCRIPT="$(cd "$(dirname "$0")/.." && pwd)/dist/controle-ds1825.sh"
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/bin" "$T/dir"
printf '#!/bin/sh\necho 000\n' > "$T/bin/curl"; chmod +x "$T/bin/curl"
cp "$SCRIPT" "$T/dir/controle-ds1825.sh"
ok=0; ko=0

# $1 = libellé, $2 = heure HHMM, $3 = attendu: "ignore" (aucun échec noté) ou "controle" (un échec noté), $4 = ntfy.env éventuel
essai() {
  rm -f "$T/dir/controle-etat" "$T/dir/controle.log" "$T/dir/ntfy.env"
  [ -n "$4" ] && printf '%s\n' "$4" > "$T/dir/ntfy.env"
  PATH="$T/bin:$PATH" CTRL_HEURE="$2" CTRL_JOUR="${5:-3}" sh "$T/dir/controle-ds1825.sh"
  if [ -f "$T/dir/controle-etat" ]; then got=controle; else got=ignore; fi
  if [ "$got" = "$3" ]; then ok=$((ok + 1)); else ko=$((ko + 1)); echo "ECHEC: $1 (heure $2, jour ${5:-3}): attendu $3, obtenu $got"; fi
}
# 5e paramètre: jour de la semaine (1 = lundi ... 7 = dimanche), 3 (mercredi) par défaut

essai "minuit pile"                      0000 ignore
essai "00:30"                            0030 ignore
essai "03:00"                            0300 ignore
essai "23:55 début de plage"             2355 ignore
essai "23:59"                            2359 ignore
essai "07:09 fin de plage"               0709 ignore
essai "07:10 la plage est finie"         0710 controle
essai "12:00"                            1200 controle
essai "23:54 avant la plage"             2354 controle
essai "plage désactivée (off) à 03:00"   0300 controle "CTRL_ARRET_DE=off"
essai "plage perso 22:00-06:00 à 22:30"  2230 ignore   "CTRL_ARRET_DE=22:00
CTRL_ARRET_A=06:00"
essai "plage perso à 06:30 (dehors)"     0630 controle "CTRL_ARRET_DE=22:00
CTRL_ARRET_A=06:00"
essai "plage sans minuit 13:00-15:00 à 14:00" 1400 ignore "CTRL_ARRET_DE=13:00
CTRL_ARRET_A=15:00"
essai "plage sans minuit à 16:00"        1600 controle "CTRL_ARRET_DE=13:00
CTRL_ARRET_A=15:00"

# Horaires de démarrage du NAS (DSM): lundi 5 h, mardi-vendredi 6 h 50, samedi-dimanche 9 h 30 (fins +20 min)
essai "lundi 05:19 (NAS pas encore démarré)"     0519 ignore   "" 1
essai "lundi 05:20 fin de plage"                 0520 controle "" 1
essai "lundi 06:00 contrôle actif"               0600 controle "" 1
essai "mardi 05:30 (démarrage à 6 h 50)"         0530 ignore   "" 2
essai "mardi 07:09"                              0709 ignore   "" 2
essai "mardi 07:10"                              0710 controle "" 2
essai "vendredi 07:09"                           0709 ignore   "" 5
essai "samedi 07:30 (démarrage à 9 h 30)"        0730 ignore   "" 6
essai "samedi 09:49"                             0949 ignore   "" 6
essai "samedi 09:50 fin de plage"                0950 controle "" 6
essai "dimanche 08:47 (alerte du 08/11 à tort)"  0847 ignore   "" 7
essai "dimanche 09:49"                           0949 ignore   "" 7
essai "dimanche 09:50"                           0950 controle "" 7
essai "dimanche 23:55 début de plage"            2355 ignore   "" 7
essai "dimanche 23:54"                           2354 controle "" 7
essai "dimanche 00:30 (vient du samedi)"         0030 ignore   "" 7
essai "fin lundi personnalisée 05:45"            0540 ignore   "CTRL_ARRET_A_LUN=05:45" 1
essai "fin week-end personnalisée 10:30"         1020 ignore   "CTRL_ARRET_A_WE=10:30" 7
essai "fin week-end perso n'affecte pas mercredi" 0720 controle "CTRL_ARRET_A_WE=10:30" 3
essai "CTRL_ARRET_A seul ne touche pas samedi"   0950 controle "CTRL_ARRET_A=08:00" 6

echo "Plage d'arrêt de nuit: $ok vérifications réussies, $ko échec(s)."
[ "$ko" -eq 0 ]

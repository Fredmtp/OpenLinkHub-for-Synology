#!/bin/bash
# Banc d'essai de superviseur.sh et controle-ds1825.sh (à lancer sous WSL, dash comme interpréteur).
#   bash test-superviseur.sh DOSSIER_DES_SCRIPTS
# Il remplace curl, start.sh et /proc par des faux et vérifie chaque cas.
set -u
SRC="${1:-.}"
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
BIN="$T/bin"; D="$T/dir"; P="$T/proc"; BEAT="$T/beat"
mkdir -p "$BIN" "$D" "$P"
export T_CURL_LOG="$T/curl.log" OLH_DIR="$D" OLH_BEAT="$BEAT" OLH_PROC="$P" PATH="$BIN:$PATH"
FAIL=0; OK=0

cat > "$BIN/curl" <<'EOF'
#!/bin/sh
echo "ARGS: $*" >> "$T_CURL_LOG"
case "$*" in *--config*) echo "STDIN: $(cat)" >> "$T_CURL_LOG";; esac
case "$*" in *%{http_code}*) printf '%s' "${FAKE_CODE:-200}";; esac
exit 0
EOF
chmod +x "$BIN/curl"
printf '#!/bin/sh\necho "start.sh $*" >> "%s/start.calls"\n' "$T" > "$D/start.sh"
printf 'NTFY_TOPIC=topic-test\nNTFY_SERVER=http://ntfy.test\n' > "$D/secrets.env"

check() { # description condition(0=ok)
  if [ "$2" = 0 ]; then OK=$((OK + 1)); else FAIL=$((FAIL + 1)); echo "ECHEC: $1"; fi
}
reset() {
  rm -rf "$P"/* "$D"/superviseur-* "$D"/maintenance "$BEAT" "$BEAT.stop" "$T/start.calls" "$T_CURL_LOG" "$D/.superviseur.verrou"
  printf 'NTFY_TOPIC=topic-test\nNTFY_SERVER=http://ntfy.test\n' > "$D/secrets.env"
}
proc() { mkdir -p "$P/$1"; echo "$2" > "$P/$1/comm"; [ -n "${3:-}" ] && printf '%s' "$3" | tr ' ' '\0' > "$P/$1/cmdline"; }
sup() { sh "$SRC/superviseur.sh"; }
calls() { [ -f "$T/start.calls" ] && wc -l < "$T/start.calls" | tr -d ' ' || echo 0; }
curls() { [ -f "$T_CURL_LOG" ] && awk '/^ARGS/ && index($0, ":27003") == 0' "$T_CURL_LOG" | wc -l | tr -d ' ' || echo 0; }  # les sondages de l'API d'OpenLinkHub ne comptent pas
fresh() { touch "$BEAT"; }
stale() { touch -d "-300 seconds" "$BEAT"; }

# 1. tout va bien
reset; proc 100 olh-watchdog; fresh; sup
check "vivante + signe de vie récent: aucune action" $([ "$(calls)" = 0 ] && [ "$(curls)" = 0 ]; echo $?)

# 2. processus disparu
reset; fresh; sup
check "processus absent: relance de la surveillance seule" $(grep -q '^start.sh surveillance$' "$T/start.calls" 2>/dev/null; echo $?)
check "processus absent: alerte ntfy priorité 5 « arretee »" $(grep -q 'Title: Surveillance du meuble arretee' "$T_CURL_LOG" && grep -q 'Priority: 5' "$T_CURL_LOG"; echo $?)
check "processus absent: adresse ntfy correcte" $(grep -q 'http://ntfy.test/topic-test' "$T_CURL_LOG"; echo $?)
check "processus absent: état « problème » mémorisé" $([ -f "$D/superviseur-probleme" ]; echo $?)

# 3. processus vivant mais signe de vie périmé
reset; proc 100 olh-watchdog; stale; sup
check "signe de vie périmé: relance" $([ "$(calls)" = 1 ]; echo $?)
check "signe de vie périmé: alerte « figee »" $(grep -q 'figee' "$T_CURL_LOG"; echo $?)

# 4. pas de signe de vie du tout (fichier absent) mais processus vivant
reset; proc 100 olh-watchdog; sup
check "fichier de signe de vie absent: relance" $([ "$(calls)" = 1 ]; echo $?)

# 5. plantage répété: 3 relances puis alerte seule, une seule fois
reset; sup; sup; sup
check "trois relances" $([ "$(calls)" = 3 ]; echo $?)
sup
check "4e passage: pas de quatrième relance" $([ "$(calls)" = 3 ]; echo $?)
check "4e passage: alerte HORS SERVICE" $(grep -q 'HORS SERVICE' "$T_CURL_LOG"; echo $?)
n=$(grep -c 'HORS SERVICE' "$T_CURL_LOG"); sup
check "5e passage: pas de deuxième alerte HORS SERVICE" $([ "$(grep -c 'HORS SERVICE' "$T_CURL_LOG")" = "$n" ]; echo $?)

# 6. rétablissement
proc 100 olh-watchdog; fresh; sup
check "rétablissement: message envoyé" $(grep -q 'retablie' "$T_CURL_LOG"; echo $?)
check "rétablissement: état effacé" $([ ! -f "$D/superviseur-probleme" ] && [ ! -f "$D/superviseur-relances" ]; echo $?)
c=$(curls); sup
check "ensuite: plus de message" $([ "$(curls)" = "$c" ]; echo $?)

# 7. veille de maintenance
reset; echo $(( $(date +%s) + 600 )) > "$D/maintenance"; sup
check "maintenance active: aucune action" $([ "$(calls)" = 0 ] && [ "$(curls)" = 0 ]; echo $?)
echo $(( $(date +%s) - 10 )) > "$D/maintenance"; sup
check "maintenance périmée: la supervision reprend" $([ "$(calls)" = 1 ]; echo $?)
reset; echo $(( $(date +%s) + 999999 )) > "$D/maintenance"; touch -d "-5 hours" "$D/maintenance"; sup
check "maintenance plafonnée à 4 h après l'écriture" $([ "$(calls)" = 1 ]; echo $?)

# 8. arrêt du NAS en cours
reset; touch "$BEAT.stop"; sup
check "arrêt en cours (.stop récent): aucune action" $([ "$(calls)" = 0 ] && [ "$(curls)" = 0 ]; echo $?)
touch -d "-2000 seconds" "$BEAT.stop"; sup
check "ancien .stop (arrêt raté): la supervision reprend" $([ "$(calls)" = 1 ]; echo $?)

# 9. start.sh en cours
reset; proc 200 sh "sh /volume1/openlinkhub/start.sh"; sup
check "start.sh en cours: aucune action" $([ "$(calls)" = 0 ] && [ "$(curls)" = 0 ]; echo $?)

# 10. jeton: jamais en ligne de commande
reset; printf 'NTFY_TOPIC=topic-test\nNTFY_TOKEN="tk_secret123"\n' > "$D/secrets.env"; sup
check "jeton absent des arguments de curl" $(! grep '^ARGS' "$T_CURL_LOG" | grep -q 'tk_secret123'; echo $?)
check "jeton transmis par l'entrée standard (--config -)" $(grep -q 'STDIN: header = "Authorization: Bearer tk_secret123"' "$T_CURL_LOG"; echo $?)
check "jeton absent du journal" $(! grep -q 'tk_secret123' "$D/superviseur.log"; echo $?)

# 11. sans sujet ntfy: pas d'envoi, la relance a lieu quand même
reset; echo "AUTRE=1" > "$D/secrets.env"; sup
check "sans NTFY_TOPIC: relance malgré tout" $([ "$(calls)" = 1 ]; echo $?)
check "sans NTFY_TOPIC: aucun appel curl" $([ "$(curls)" = 0 ]; echo $?)

# ---- OpenLinkHub (API sur le port 27003): relance seule du Hub ----
now_() { date +%s; }
hubdown() { echo $(( $(now_) - ${1:-200} )) > "$D/superviseur-hub-depuis"; echo $(( $(now_) - 30 )) > "$D/superviseur-hub-vu"; }
hubcalls() { [ -f "$T/start.calls" ] && grep -c '^start.sh hub$' "$T/start.calls" || echo 0; }

# 13. API qui répond: aucune action
reset; proc 100 olh-watchdog; fresh; FAKE_CODE=200 sup
check "hub: API 200, aucune action" $([ "$(calls)" = 0 ] && [ "$(curls)" = 0 ]; echo $?)

# 14. API muette: décompte d'abord, relance après 3 minutes
reset; proc 100 olh-watchdog; fresh; FAKE_CODE=000 sup
check "hub: 1er constat, pas de relance (décompte lancé)" $([ "$(calls)" = 0 ] && [ -f "$D/superviseur-hub-depuis" ]; echo $?)
hubdown 60; FAKE_CODE=000 sup
check "hub: muet depuis 60 s, pas de relance" $([ "$(calls)" = 0 ]; echo $?)
hubdown 200; fresh; FAKE_CODE=000 sup
check "hub: muet depuis 200 s, relance de start.sh hub" $([ "$(hubcalls)" = 1 ]; echo $?)
check "hub: la surveillance n'est pas relancée en même temps" $(! grep -q '^start.sh surveillance$' "$T/start.calls"; echo $?)
check "hub: alerte ntfy priorité 5" $(grep -q 'Title: OpenLinkHub ne repond plus, relance' "$T_CURL_LOG" && grep -q 'Priority: 5' "$T_CURL_LOG"; echo $?)
check "hub: la 2e relance devra attendre 3 min de plus" $([ $(( $(now_) - $(cat "$D/superviseur-hub-depuis") )) -lt 10 ]; echo $?)

# 15. vieux constat (superviseur absent > 150 s: maintenance, démarrage, NAS éteint): pas de relance immédiate
reset; proc 100 olh-watchdog; fresh
echo $(( $(now_) - 5000 )) > "$D/superviseur-hub-depuis"; echo $(( $(now_) - 5000 )) > "$D/superviseur-hub-vu"
FAKE_CODE=000 sup
check "hub: constat périmé (> 150 s sans regarder): décompte remis à zéro" $([ "$(calls)" = 0 ]; echo $?)

# 16. au plus 3 relances par heure, puis alerte seule (une fois)
reset; proc 100 olh-watchdog
for i in 1 2 3; do fresh; hubdown 200; FAKE_CODE=000 sup; done
check "hub: trois relances" $([ "$(hubcalls)" = 3 ]; echo $?)
fresh; hubdown 200; FAKE_CODE=000 sup
check "hub: pas de 4e relance" $([ "$(hubcalls)" = 3 ]; echo $?)
check "hub: alerte HORS SERVICE" $(grep -q 'Title: OpenLinkHub HORS SERVICE' "$T_CURL_LOG"; echo $?)
n=$(grep -c 'OpenLinkHub HORS SERVICE' "$T_CURL_LOG"); fresh; hubdown 200; FAKE_CODE=000 sup
check "hub: pas de 2e alerte HORS SERVICE dans l'heure" $([ "$(grep -c 'OpenLinkHub HORS SERVICE' "$T_CURL_LOG")" = "$n" ]; echo $?)

# 17. rétablissement
fresh; FAKE_CODE=200 sup
check "hub: message de rétablissement" $(grep -q 'Title: OpenLinkHub retabli' "$T_CURL_LOG"; echo $?)
check "hub: état effacé" $([ ! -f "$D/superviseur-hub-probleme" ] && [ ! -f "$D/superviseur-hub-relances" ] && [ ! -f "$D/superviseur-hub-depuis" ]; echo $?)
c=$(curls); fresh; FAKE_CODE=200 sup
check "hub: ensuite, plus de message" $([ "$(curls)" = "$c" ]; echo $?)

# 18. maintenance, start.sh en cours, contrôle désactivé: aucune relance
reset; proc 100 olh-watchdog; fresh; hubdown 200; echo $(( $(now_) + 600 )) > "$D/maintenance"; FAKE_CODE=000 sup
check "hub: maintenance active, aucune relance" $([ "$(calls)" = 0 ]; echo $?)
reset; proc 100 olh-watchdog; proc 200 sh "sh /volume1/openlinkhub/start.sh"; fresh; hubdown 200; FAKE_CODE=000 sup
check "hub: start.sh en cours, aucune relance" $([ "$(calls)" = 0 ]; echo $?)
reset; proc 100 olh-watchdog; fresh; hubdown 200; OLH_HUB_MAX=0 FAKE_CODE=000 sup
check "hub: OLH_HUB_MAX=0 désactive le contrôle" $([ "$(calls)" = 0 ]; echo $?)

# 19. les deux pannes en même temps: surveillance ET hub relancés
reset; hubdown 200; FAKE_CODE=000 sup
check "hub: surveillance absente et API muette: les deux relances" $(grep -q '^start.sh surveillance$' "$T/start.calls" && [ "$(hubcalls)" = 1 ]; echo $?)

# 12. verrou
reset; mkdir "$D/.superviseur.verrou"; sup
check "verrou récent: un seul superviseur à la fois" $([ "$(calls)" = 0 ]; echo $?)
touch -d "-10 minutes" "$D/.superviseur.verrou"; sup
check "verrou périmé: la supervision reprend" $([ "$(calls)" = 1 ]; echo $?)

# ---- controle-ds1825.sh (sur le DS1522+) ----
C="$T/ctrl"; mkdir -p "$C"; cp "$SRC/controle-ds1825.sh" "$C/"; printf 'NTFY_TOPIC=topic-test\n' > "$C/ntfy.env"
run_ctrl() { sh "$C/controle-ds1825.sh"; }
rm -f "$T_CURL_LOG"
FAKE_CODE=200 run_ctrl
check "contrôle: page joignable, rien à signaler" $([ "$(curls)" = 1 ] && ! grep -q 'Title' "$T_CURL_LOG"; echo $?)
for i in 1 2; do FAKE_CODE=000 run_ctrl; done
check "contrôle: pas d'alerte avant 3 échecs" $(! grep -q 'Title' "$T_CURL_LOG"; echo $?)
FAKE_CODE=000 run_ctrl
check "contrôle: alerte au 3e échec" $(grep -q 'Title: DS1825+ ne repond plus' "$T_CURL_LOG"; echo $?)
a=$(grep -c 'ne repond plus' "$T_CURL_LOG"); FAKE_CODE=000 run_ctrl
check "contrôle: pas de nouvelle alerte avant le rappel" $([ "$(grep -c 'ne repond plus' "$T_CURL_LOG")" = "$a" ]; echo $?)
sed -i '3s/.*/1/' "$C/controle-etat"; FAKE_CODE=000 run_ctrl
check "contrôle: rappel quand le délai est écoulé" $([ "$(grep -c 'ne repond plus' "$T_CURL_LOG")" = $((a + 1)) ]; echo $?)
FAKE_CODE=200 run_ctrl
check "contrôle: message de rétablissement" $(grep -q 'Surveillance du DS1825+ retablie' "$T_CURL_LOG"; echo $?)
check "contrôle: état effacé" $([ ! -f "$C/controle-etat" ]; echo $?)
rm -f "$T_CURL_LOG"; touch "$C/pause"; FAKE_CODE=000 run_ctrl
check "contrôle: fichier pause = aucune vérification" $([ ! -f "$T_CURL_LOG" ]; echo $?)

rm -f "$C/pause" "$C/controle-etat" "$T_CURL_LOG"
out=$(FAKE_CODE=200 sh "$C/controle-ds1825.sh" test)
check "contrôle (essai): affiche le code de réponse" $(echo "$out" | grep -q 'code HTTP 200'; echo $?)
check "contrôle (essai): envoie le message de test" $(grep -q 'Title: TEST controle du DS1825+' "$T_CURL_LOG"; echo $?)
check "contrôle (essai): ne crée pas d'état" $([ ! -f "$C/controle-etat" ]; echo $?)
touch "$C/pause"; rm -f "$T_CURL_LOG"; FAKE_CODE=000 sh "$C/controle-ds1825.sh" test > /dev/null
check "contrôle (essai): fonctionne même en pause" $(grep -q 'TEST controle' "$T_CURL_LOG"; echo $?)
rm -f "$C/ntfy.env"; out=$(FAKE_CODE=200 sh "$C/controle-ds1825.sh" test)
check "contrôle (essai): sans ntfy.env, le dit clairement" $(echo "$out" | grep -q 'ntfy.env introuvable'; echo $?)

echo "Réussis: $OK, échecs: $FAIL"
[ "$FAIL" = 0 ]

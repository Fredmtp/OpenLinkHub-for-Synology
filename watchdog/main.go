// watchdog surveille la ventilation du meuble du NAS via l'API d'OpenLinkHub
// et éteint le NAS si les ventilateurs sont tous à l'arrêt ou si une sonde
// du Commander DUO dépasse un seuil.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type device struct {
	ChannelID          int     `json:"channelId"`
	Name               string  `json:"name"`
	Description        string  `json:"description"`
	RPM                int     `json:"rpm"`
	Temperature        float64 `json:"temperature"`
	HasSpeed           bool    `json:"HasSpeed"`
	IsTemperatureProbe bool    `json:"IsTemperatureProbe"`
}

type apiResponse struct {
	Code    int `json:"code"`
	Devices map[string]struct {
		GetDevice json.RawMessage `json:"GetDevice"`
	} `json:"devices"`
}

type probe struct {
	label     string
	channel   int
	sustained float64
	instant   float64
	hotSince  time.Time
	instantN  int
}

var (
	baseURL     = flag.String("url", "http://192.168.1.10:27003", "adresse de l'API OpenLinkHub")
	interval    = flag.Duration("interval", 10*time.Second, "délai entre deux mesures")
	grace       = flag.Duration("grace", 120*time.Second, "délai après le démarrage avant de pouvoir éteindre le NAS")
	minRPM      = flag.Int("min-rpm", 100, "vitesse en dessous de laquelle un ventilateur est considéré arrêté")
	fanLostFor  = flag.Duration("fan-lost-for", 15*time.Minute, "durée de perte TOTALE de la ventilation (aucun ventilateur en rotation, ou tous les extracteurs arrêtés ou absents) avant l'arrêt du NAS")
	sustain     = flag.Duration("sustain", 3*time.Minute, "durée au-dessus du seuil prolongé avant l'arrêt du NAS")
	nasCh       = flag.Int("nas-ch", 13, "canal du Commander DUO dont la sonde est dans le NAS")
	qnapVendor  = flag.String("qnap-vendor", "", "fabricant de la baie USB à surveiller dans /proc/scsi/scsi (« QNAP »): alerte si elle disparaît ou si le noyau signale des erreurs USB; vide = désactivé")
	qnapCh      = flag.Int("qnap-ch", 14, "canal du Commander DUO dont la sonde est dans la baie QNAP")
	nasSust     = flag.Float64("nas-sustained", 45, "NAS: seuil prolongé en °C")
	nasInst     = flag.Float64("nas-instant", 50, "NAS: seuil immédiat en °C")
	qnapSust    = flag.Float64("qnap-sustained", 45, "QNAP: seuil prolongé en °C")
	qnapInst    = flag.Float64("qnap-instant", 50, "QNAP: seuil immédiat en °C")
	cpuMax      = flag.Float64("cpu-max", 85, "processeur du NAS: seuil prolongé en °C (couche indépendante d'OpenLinkHub)")
	unreachable = flag.Duration("unreachable", 5*time.Minute, "durée sans réponse de l'API avant alerte dans le journal")
	poweroffCmd = flag.String("poweroff-cmd", "/sbin/poweroff", "commande d'arrêt du NAS")
	logPath     = flag.String("log", "", "fichier journal (en plus de la sortie standard)")
	dry         = flag.Bool("dry", false, "mode à blanc: n'éteint rien, journalise seulement")
	once        = flag.Bool("once", false, "une seule mesure affichée, puis fin")

	secretsPath  = flag.String("secrets", "", "fichier CLE=valeur des notifications (ntfy, Graph); vide = notifications désactivées")
	listenAddr   = flag.String("listen", "", "adresse:port de la page d'état (ex. 192.168.1.10:27004); vide = pas de page")
	digestHours  = flag.String("digest-hours", "8,13,19", "heures des résumés par notification")
	collectEvery = flag.Duration("collect", time.Minute, "délai entre deux relevés des capteurs du NAS (disques, onduleur…)")
	diskHold     = flag.Duration("disk-hold", time.Minute, "durée au-dessus du seuil d'arrêt disque avant l'arrêt du NAS")
	boardHold    = flag.Duration("board-hold", 3*time.Minute, "durée au-dessus du seuil d'arrêt carte mère avant l'arrêt du NAS")
	sensorsOnce  = flag.Bool("sensors", false, "relève les capteurs du NAS, affiche les anomalies, puis fin")
	testNotif    = flag.Bool("test-notif", false, "envoie un message de test sur chaque canal, puis fin")
	previewDir   = flag.String("preview", "", "écrit des mails d'exemple (avec l'IA si une clé est présente) dans ce dossier, puis fin; rien n'est envoyé")
	eventsFile   = flag.String("events", "", "journal des événements (JSON, une ligne par événement, gardé 7 jours): sert à l'IA et à la page d'état")
	noAI         = flag.Bool("no-ai", false, "désactive l'analyse par l'IA: alertes au texte standard")
	aiMaxCalls   = flag.Int("ai-max-calls", 40, "nombre maximal d'appels à l'IA par jour (0 = sans limite)")
	knownFile    = flag.String("known", "", "fichier des situations déjà connues (« clé | note » par ligne): jamais poussées, rappelées chaque semaine")
	maintFile    = flag.String("maintenance", "", "fichier de veille de l'arrêt ventilation (date de fin en secondes Unix, écrit par maintenance.sh; 4 h au plus)")
	stateFile    = flag.String("state", "", "fichier d'état (compteurs de retraits d'urgence des disques) pour détecter les coupures brutales, même pendant un arrêt de la surveillance")

	huePlug       = flag.String("hue-plug", "", "nom de la prise Philips Hue qui alimente les ventilateurs du meuble (HUE_BRIDGE et HUE_KEY dans secrets.env); vide = prise non surveillée")
	hueEvery      = flag.Duration("hue-poll", 20*time.Second, "intervalle de lecture de l'état de la prise Hue")
	hubRestartCmd = flag.String("hub-restart-cmd", "", "commande qui relance OpenLinkHub seul (sh start.sh hub), lancée 20 s après un cycle de la prise Hue")

	recordDir       = flag.String("record", "", "dossier des relevés de températures (un fichier CSV par mois, une ligne par minute); vide = pas d'enregistrement")
	buzzerPath      = flag.String("buzzer", "", "port série du buzzer du NAS (ex. /dev/ttyS1: le caractère 2 donne un bip court); vide = pas d'alerte sonore")
	beepGap         = flag.Duration("beep-gap", 300*time.Millisecond, "délai entre deux bips d'une série")
	beepCount       = flag.Int("beep-count", 5, "nombre de bips d'une série d'alerte")
	beepPause       = flag.Duration("beep-pause", 2*time.Second, "silence entre deux séries d'alerte")
	beepNotice      = flag.Duration("beep-notice", 5*time.Minute, "délai entre deux bips isolés quand la ventilation est partiellement en panne (0 = aucun)")
	silenceFile     = flag.String("silence", "", "fichier de silence de l'alerte sonore (date de fin en secondes Unix, écrit par silence.sh; 4 h au plus)")
	identifyMode    = flag.Bool("identify", false, "allume un ventilateur à la fois (profil de lumière -identify-profile) pour repérer leur position, remet les profils d'origine, puis fin")
	identifyProfile = flag.String("identify-profile", "circle", "profil RGB utilisé par -identify")
	identifySecs    = flag.Int("identify-secs", 10, "durée d'allumage de chaque ventilateur avec -identify, en secondes")
	demoMode        = flag.Bool("demo-alternate", false, "joue l'alternance des trois extracteurs (de gauche à droite) à trois rythmes (2, 4 et 8 s) pour choisir, remet les profils d'origine, puis fin")
	ledOrder        = flag.String("led-order", "1,3,2", "canaux des extracteurs de gauche à droite (relevé du 28/10/2026: 1 = gauche, 3 = centre, 2 = droite)")
	demoTours       = flag.Int("demo-tours", 3, "nombre de tours complets de l'alternance par rythme avec -demo-alternate")
	dsmNotify       = flag.Bool("dsm-notify", false, "envoie aussi les messages importants (priorité 4 et 5) dans le centre de notifications de DSM (synodsmnotify, groupe @administrators)")
	autoStopFile    = flag.String("autostop", "", "marqueur du dernier arrêt automatique pour perte de ventilation (protection contre la boucle d'arrêts); vide = pas de protection")
	autoStopGuard   = flag.Duration("autostop-guard", 6*time.Hour, "durée pendant laquelle l'arrêt par la ventilation est suspendu après un arrêt automatique (l'arrêt par la température reste actif)")
)

// lim regroupe les seuils; les options correspondantes les modifient.
var lim = defaultLimits()

func init() {
	flag.Float64Var(&lim.DiskWarn, "disk-warn", lim.DiskWarn, "disque: alerte en °C")
	flag.Float64Var(&lim.DiskCrit, "disk-crit", lim.DiskCrit, "disque: alerte critique en °C")
	flag.Float64Var(&lim.DiskShutdown, "disk-shutdown", lim.DiskShutdown, "disque: arrêt du NAS si maintenu (°C)")
	flag.Float64Var(&lim.DiskInstant, "disk-instant", lim.DiskInstant, "disque: arrêt du NAS sur deux relevés (°C)")
	flag.Float64Var(&lim.CPUWarn, "cpu-warn", lim.CPUWarn, "processeur: alerte en °C")
	flag.Float64Var(&lim.BoardWarn, "board-warn", lim.BoardWarn, "carte mère: alerte en °C")
	flag.Float64Var(&lim.BoardShutdown, "board-shutdown", lim.BoardShutdown, "carte mère: arrêt du NAS si maintenu (°C)")
	flag.Float64Var(&lim.BoardInstant, "board-instant", lim.BoardInstant, "carte mère: arrêt du NAS sur deux relevés (°C)")
	flag.Float64Var(&lim.ProbeWarn, "probe-warn", lim.ProbeWarn, "sondes du meuble: alerte en °C")
	flag.Float64Var(&lim.ProbeCrit, "probe-crit", lim.ProbeCrit, "sondes du meuble: alerte critique en °C")
	flag.DurationVar(&lim.ProbeFrozen, "probe-frozen", lim.ProbeFrozen, "sondes du meuble: alerte si la valeur reste STRICTEMENT identique pendant cette durée (0 = désactivé)")
	flag.IntVar(&lim.CorsairFans, "corsair-fans", lim.CorsairFans, "nombre de ventilateurs Corsair attendus (0 = ne pas contrôler)")
	flag.IntVar(&lim.ExtractionStop, "extraction-stop", lim.ExtractionStop, "nombre d'extracteurs arrêtés ou absents qui rend la ventilation « dégradée » (alerte critique; l'arrêt ne vient que de la température; 0 = règle désactivée)")
	flag.IntVar(&lim.IntakeFan, "intake-ch", lim.IntakeFan, "canal du ventilateur d'admission (0 = non suivi)")
	flag.Func("extraction-ch", "canaux des ventilateurs d'extraction, séparés par des virgules (défaut 1,2,3; l'admission, canal 15, n'en fait pas partie)", func(s string) error {
		var out []int
		for _, p := range strings.Split(s, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil {
				return err
			}
			out = append(out, n)
		}
		lim.ExtractionFans = out
		return nil
	})
}

func logf(format string, a ...any) { log.Printf(format, a...) }

var client = &http.Client{Timeout: 5 * time.Second}

func fetch() ([]device, error) {
	resp, err := client.Get(*baseURL + "/api/devices/")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	var out []device
	for _, entry := range r.Devices {
		var hub struct {
			Devices map[string]device `json:"devices"`
		}
		if json.Unmarshal(entry.GetDevice, &hub) != nil {
			continue
		}
		for _, d := range hub.Devices {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChannelID < out[j].ChannelID })
	return out, nil
}

func isFan(d device) bool {
	return d.HasSpeed && !d.IsTemperatureProbe && d.Description == "Fan"
}

// cpuTemp lit la température du processeur du NAS (pilote k10temp), sans passer par OpenLinkHub.
func cpuTemp() (float64, bool) {
	names, _ := filepath.Glob("/sys/class/hwmon/hwmon*/name")
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil || strings.TrimSpace(string(b)) != "k10temp" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(n), "temp1_input"))
		if err != nil {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			continue
		}
		return float64(v) / 1000, true
	}
	return 0, false
}

// cpuCheck est la couche indépendante d'OpenLinkHub: elle éteint le NAS si le
// processeur reste trop chaud, même quand la ventilation n'est plus mesurable.
func (m *monitor) cpuCheck(t float64, now time.Time) string {
	if t < *cpuMax {
		if !m.cpuHotSince.IsZero() {
			log.Printf("Processeur redescendu à %.1f °C", t)
		}
		m.cpuHotSince = time.Time{}
		return ""
	}
	if m.cpuHotSince.IsZero() {
		m.cpuHotSince = now
		log.Printf("ATTENTION: processeur à %.1f °C (seuil %.0f °C)", t, *cpuMax)
	}
	if now.Sub(m.cpuHotSince) >= *sustain {
		return fmt.Sprintf("processeur au-dessus de %.0f °C depuis %s (%.1f °C)", *cpuMax, now.Sub(m.cpuHotSince).Round(time.Second), t)
	}
	return ""
}

type monitor struct {
	cpuHotSince time.Time
	started     time.Time
	seenFan     map[int]bool
	fanDown     map[int]bool
	deadSince   time.Time
	vent        ventState
	// fanGuardUntil: fin de la suspension de l'arrêt par la ventilation (voir loopguard.go).
	fanGuardUntil time.Time
	// fanStop est vrai quand la raison renvoyée par le dernier evaluate vient de la perte de ventilation.
	fanStop   bool
	probes    []*probe
	warned    map[string]bool
	diskHeat  *heat
	boardHeat *heat
}

// nasCheck est la couche thermique du NAS lui-même: disques et carte mère. Elle ne dépend ni
// d'OpenLinkHub ni du Commander DUO. Un relevé donné n'est compté qu'une fois (voir heat.update).
func (m *monitor) nasCheck(s nasSnap, now time.Time) string {
	if m.diskHeat == nil {
		m.diskHeat = &heat{sustained: lim.DiskShutdown, instant: lim.DiskInstant}
		m.boardHeat = &heat{sustained: lim.BoardShutdown, instant: lim.BoardInstant}
	}
	if now.Sub(m.started) < *grace {
		return ""
	}
	reason := ""
	if name, t, ok := s.maxDiskTemp(); ok {
		m.diskHeat.label = "disque " + name
		if r := m.diskHeat.update(t, s.At, now, *diskHold); r != "" {
			reason = r
		}
	}
	if name, t, ok := s.maxBoardTemp(); ok {
		m.boardHeat.label = "carte mère (" + name + ")"
		if r := m.boardHeat.update(t, s.At, now, *boardHold); r != "" && reason == "" {
			reason = r
		}
	}
	return reason
}

// warnOnce évite de répéter le même avertissement à chaque mesure.
func (m *monitor) warnOnce(key, msg string) {
	if !m.warned[key] {
		m.warned[key] = true
		log.Printf("ATTENTION: %s", msg)
	}
}

func (m *monitor) clear(key string) { delete(m.warned, key) }

// evaluate renvoie une raison d'arrêt non vide si le NAS doit être éteint.
func (m *monitor) evaluate(devs []device, now time.Time) string {
	m.fanStop = false
	enforce := now.Sub(m.started) >= *grace

	running, total := 0, 0
	for _, d := range devs {
		if !isFan(d) {
			continue
		}
		total++
		if d.RPM >= *minRPM {
			running++
			m.seenFan[d.ChannelID] = true
			if m.fanDown[d.ChannelID] {
				log.Printf("Ventilateur canal %d reparti (%d tr/min)", d.ChannelID, d.RPM)
				delete(m.fanDown, d.ChannelID)
			}
		} else if m.seenFan[d.ChannelID] && !m.fanDown[d.ChannelID] {
			m.fanDown[d.ChannelID] = true
			log.Printf("ATTENTION: ventilateur canal %d arrêté (%d tr/min)", d.ChannelID, d.RPM)
		}
	}

	// Règle d'arrêt (décision du 28/10/2026): seule la PERTE TOTALE de la ventilation (aucun ventilateur
	// en rotation, ou tous les extracteurs arrêtés ou ABSENTS de la liste: on ne peut pas savoir s'ils
	// tournent) éteint le NAS, après -fan-lost-for. Une ventilation partielle ou dégradée alerte
	// seulement (assess); l'arrêt ne vient alors que de la température des disques (nasCheck).
	l := lim
	l.MinRPM = *minRPM
	vent, down, _ := ventilationState(devs, l)
	if vent != m.vent {
		if vent != ventOK {
			log.Printf("ATTENTION: ventilation %s (extracteurs arrêtés ou absents: %s)", vent, orDash(joinChannels(down)))
		} else {
			log.Printf("Ventilation normale")
		}
		m.vent = vent
	}

	until, maint := maintenanceUntil(*maintFile, now)
	if maint {
		m.warnOnce("maint", fmt.Sprintf("maintenance: arrêt ventilation en veille jusqu'à %s", until.Format("15:04")))
	} else if m.warned["maint"] {
		log.Printf("Maintenance terminée: arrêt ventilation rétabli")
		m.clear("maint")
	}

	reason := ""
	switch {
	case total == 0:
		// Liste vide: la mesure est impossible, ce qui n'est pas une panne prouvée.
		// Le contrôle de la température du processeur (cpuCheck) reste actif.
		m.warnOnce("nofan", "aucun ventilateur listé par OpenLinkHub: mesure impossible, pas d'arrêt tant que le processeur reste froid")
		m.deadSince = time.Time{}
	case maint:
		// Veille: le compte à rebours repart de zéro à la fin de la maintenance.
		m.clear("nofan")
		m.deadSince = time.Time{}
	case vent == ventLost:
		m.clear("nofan")
		what := fmt.Sprintf("aucun ventilateur en rotation (%d détecté(s))", total)
		if running > 0 {
			what = fmt.Sprintf("tous les ventilateurs d'extraction (canaux %s) sont arrêtés ou absents", joinChannels(down))
		}
		if m.deadSince.IsZero() {
			m.deadSince = now
			log.Printf("ATTENTION: ventilation perdue: %s (arrêt du NAS dans %s si elle n'est pas rétablie)", what, *fanLostFor)
		}
		if enforce && now.Sub(m.deadSince) >= *fanLostFor {
			if now.Before(m.fanGuardUntil) {
				m.warnOnce("loopguard", fmt.Sprintf("%s depuis %s: pas de nouvel arrêt avant %s (protection contre la boucle d'arrêts), la température des disques reste surveillée",
					what, now.Sub(m.deadSince).Round(time.Second), m.fanGuardUntil.Format("15:04")))
			} else {
				reason = fmt.Sprintf("%s depuis %s", what, now.Sub(m.deadSince).Round(time.Second))
				m.fanStop = true
			}
		}
	default:
		m.clear("nofan")
		m.clear("loopguard")
		if !m.deadSince.IsZero() {
			log.Printf("Ventilation rétablie (%d ventilateur(s) en rotation)", running)
		}
		m.deadSince = time.Time{}
	}

	for _, p := range m.probes {
		var d *device
		for i := range devs {
			if devs[i].ChannelID == p.channel && devs[i].IsTemperatureProbe {
				d = &devs[i]
			}
		}
		key := fmt.Sprintf("probe%d", p.channel)
		if d == nil || d.Temperature <= 0 || d.Temperature > 120 {
			if enforce {
				m.warnOnce(key, fmt.Sprintf("sonde %s (canal %d) absente ou illisible", p.label, p.channel))
			}
			continue
		}
		m.clear(key)
		t := d.Temperature
		if t >= p.instant {
			p.instantN++
		} else {
			p.instantN = 0
		}
		if t >= p.sustained {
			if p.hotSince.IsZero() {
				p.hotSince = now
				log.Printf("ATTENTION: sonde %s à %.1f °C (seuil prolongé %.0f °C)", p.label, t, p.sustained)
			}
		} else {
			if !p.hotSince.IsZero() {
				log.Printf("Sonde %s redescendue à %.1f °C", p.label, t)
			}
			p.hotSince = time.Time{}
		}
		if enforce && reason == "" {
			switch {
			case p.instantN >= 2:
				reason = fmt.Sprintf("sonde %s à %.1f °C, seuil immédiat %.0f °C", p.label, t, p.instant)
			case !p.hotSince.IsZero() && now.Sub(p.hotSince) >= *sustain:
				reason = fmt.Sprintf("sonde %s au-dessus de %.0f °C depuis %s (%.1f °C)", p.label, p.sustained, now.Sub(p.hotSince).Round(time.Second), t)
			}
		}
	}
	return reason
}

func summary(devs []device) string {
	s := ""
	for _, d := range devs {
		switch {
		case isFan(d):
			s += fmt.Sprintf("  ventilateur canal %d: %d tr/min\n", d.ChannelID, d.RPM)
		case d.IsTemperatureProbe:
			s += fmt.Sprintf("  sonde canal %d: %.1f °C\n", d.ChannelID, d.Temperature)
		}
	}
	return s
}

// nt est le canal de notification (désactivé si aucun secret n'est fourni).
var nt *notifier

// disp répartit les alertes (immédiates, résumé, déjà connues) et appelle l'IA.
var disp *dispatcher

// pageURL est l'adresse de la page d'état à mettre dans les mails (vide sans -listen).
func pageURL() string {
	if *listenAddr == "" {
		return ""
	}
	return "http://" + *listenAddr + "/"
}

func shutdown(reason string) {
	log.Printf("ARRET DU NAS: %s", reason)
	if *dry {
		log.Printf("(mode à blanc: aucune extinction)")
		return
	}
	// Prévient d'abord (au plus 15 s), puis éteint: la notification ne doit jamais retarder l'arrêt.
	if nt != nil {
		nt.sendNow(shutdownMessage(reason, pageURL()), 15*time.Second)
	}
	markStopping(*heartbeatFile) // le superviseur ne relance pas la surveillance pendant l'arrêt du NAS
	if out, err := exec.Command(*poweroffCmd).CombinedOutput(); err != nil {
		log.Printf("ECHEC de la commande d'arrêt: %v %s", err, out)
		return
	}
	time.Sleep(10 * time.Minute)
}

// safely exécute f en journalisant une éventuelle panique: une goroutine annexe ne doit jamais
// faire tomber la surveillance de sécurité.
func safely(what string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ERREUR interne (%s): %v", what, r)
		}
	}()
	f()
}

func main() {
	flag.Parse()
	log.SetFlags(log.Ldate | log.Ltime)
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			log.Fatalf("journal impossible: %v", err)
		}
		log.SetOutput(io.MultiWriter(os.Stdout, f))
	}

	if *identifyMode {
		os.Exit(runIdentify(*baseURL, *identifyProfile, *identifySecs))
	}
	if *demoMode {
		os.Exit(runDemoAlternate(*baseURL, *identifyProfile, *ledOrder, *demoTours))
	}

	var notes []string
	nt, notes = newNotifier(*secretsPath)
	if *dsmNotify {
		nt.senders = append(nt.senders, newDSMSender())
		notes = append(notes, "notifications DSM actives (priorité 4 et 5)")
	}

	if *testNotif {
		if !nt.enabled() {
			log.Fatalf("aucun canal actif: %s", strings.Join(notes, "; "))
		}
		m := message{Subject: "[NAS meuble] message de test", Priority: 3, Tags: []string{"white_check_mark"},
			Body:  fmt.Sprintf("Message de test envoyé le %s.\nSi vous le lisez, ce canal fonctionne.\n", time.Now().Format("02/01/2006 à 15:04")),
			Short: "Message de test: ce canal fonctionne."}
		failed := false
		for _, s := range nt.senders {
			if err := s.Send(m); err != nil {
				fmt.Printf("%s: ÉCHEC: %v\n", s.Name(), err)
				failed = true
			} else {
				fmt.Printf("%s: OK\n", s.Name())
			}
		}
		if failed {
			os.Exit(1)
		}
		return
	}

	if *previewDir != "" {
		var an aiAnalyzer
		if !*noAI {
			if a := newAnthropic(readEnvFile(*secretsPath), *aiMaxCalls); a != nil {
				an = a
				fmt.Printf("Analyse IA active (modèle %s): les textes sont rédigés par l'IA.\n", a.model)
			} else {
				fmt.Println("Pas de clé ANTHROPIC_API_KEY: textes standard.")
			}
		}
		if err := runPreview(*previewDir, an); err != nil {
			log.Fatalf("aperçu impossible: %v", err)
		}
		if a, ok := an.(*anthropicAI); ok {
			s := a.stats()
			fmt.Printf("Appels à l'IA: %d, échecs: %d, jetons: %d en entrée, %d en sortie. Dernière erreur: %q\n",
				s.CallsToday, s.Failures, s.TokensIn, s.TokensOut, s.LastError)
		}
		return
	}

	if *sensorsOnce {
		s := collectNAS()
		fmt.Print(describeNAS(s))
		devs, err := fetch()
		in := assessInput{NAS: s, HaveNAS: true, Devs: devs, DevsOK: err == nil, Enforce: true, NasCh: *nasCh, QnapCh: *qnapCh}
		if err != nil {
			fmt.Printf("OpenLinkHub: injoignable (%v)\n", err)
		} else {
			fmt.Print(summary(devs))
		}
		fs := assess(in, lim)
		if len(fs) == 0 {
			fmt.Println("Anomalies: aucune")
		}
		for _, f := range fs {
			fmt.Printf("Anomalie [%s] %s\n", f.Level, f.Text)
		}
		return
	}

	m := &monitor{
		started: time.Now(),
		seenFan: map[int]bool{},
		fanDown: map[int]bool{},
		warned:  map[string]bool{},
		probes: []*probe{
			{label: "NAS", channel: *nasCh, sustained: *nasSust, instant: *nasInst},
			{label: "QNAP", channel: *qnapCh, sustained: *qnapSust, instant: *qnapInst},
		},
	}

	if *once {
		devs, err := fetch()
		if err != nil {
			log.Fatalf("API injoignable: %v", err)
		}
		fmt.Print(summary(devs))
		*grace = 0
		if r := m.evaluate(devs, time.Now()); r != "" {
			fmt.Println("Verdict: ARRET -", r)
		} else {
			fmt.Println("Verdict: OK")
		}
		return
	}

	log.Printf("Surveillance démarrée (dry=%v, grace=%s, NAS %.0f/%.0f °C, QNAP %.0f/%.0f °C, disques %.0f/%.0f °C)",
		*dry, *grace, *nasSust, *nasInst, *qnapSust, *qnapInst, lim.DiskShutdown, lim.DiskInstant)
	log.Printf("Notifications: %s", strings.Join(notes, ", "))

	st := &shared{}
	hist := newHistory()
	hours := parseHours(*digestHours)
	first := 8
	if len(hours) > 0 {
		first = hours[0]
		for _, h := range hours {
			if h < first {
				first = h
			}
		}
	}
	var an aiAnalyzer
	if !*noAI {
		if a := newAnthropic(readEnvFile(*secretsPath), *aiMaxCalls); a != nil {
			an = a
			log.Printf("Analyse IA: active (modèle %s, %d appels par jour au plus)", a.model, *aiMaxCalls)
		} else {
			log.Printf("Analyse IA: désactivée (ANTHROPIC_API_KEY absente): alertes au texte standard")
		}
	}
	disp = newDispatcher(nt.post, an, *dry, pageURL(), loadKnown(*knownFile),
		func() (assessInput, []finding) { return st.view(time.Now()) }, hist, first)
	disp.knownPath = *knownFile // le fichier est relu à chaque alerte et à chaque résumé
	events := newEventLog(*eventsFile, time.Now())
	disp.events = events
	log.Printf("Situations connues (jamais poussées): %d; événements gardés: %d", len(disp.known), len(events.since(time.Now().Add(-7*24*time.Hour))))
	al := newAlerter(disp.post)
	dig := newDigester(hours)
	go nt.work()

	// Prise Hue des ventilateurs: lecture régulière de son état, rallumage et cycle de réparation (hueguard.go).
	var hc *hueCtl
	if *huePlug != "" {
		env := readEnvFile(*secretsPath)
		if cl := newHueClient(env["HUE_BRIDGE"], env["HUE_KEY"], *huePlug); cl != nil {
			var after func() error
			if *hubRestartCmd != "" {
				cmd := *hubRestartCmd
				after = func() error { return detachedRun(cmd) }
			}
			hc = newHueCtl(cl, newHueGuard(defaultHueCfg()), st, disp.post, events, *dry, after, pageURL())
			go hc.poll(*hueEvery)
			log.Printf("Prise Hue « %s »: état lu toutes les %s; rallumée si elle s'éteint, cycle si le Hub se bloque", *huePlug, *hueEvery)
		} else {
			log.Printf("ATTENTION: prise Hue non surveillée (HUE_BRIDGE ou HUE_KEY absents de secrets.env)")
		}
	}

	var rec *recorder
	if *recordDir != "" {
		rec = newRecorder(*recordDir)
		log.Printf("Relevés de températures: %s (un fichier par mois, une ligne par relevé)", *recordDir)
	}
	var testBeep func() // un bip, pour l'essai depuis la page de commandes
	beepCfg := alarmCfg{Gap: *beepGap, Count: *beepCount, Pause: *beepPause, Notice: *beepNotice}
	if *buzzerPath != "" {
		var lastBeepErr string
		c := beepCfg
		testBeep = ttyBeep(*buzzerPath, &lastBeepErr)
		go runAlarm(st, c, testBeep)
		log.Printf("Alerte sonore: %s (%d bips espacés de %s, silence de %s; bip isolé toutes les %s si ventilation partielle)",
			*buzzerPath, c.Count, c.Gap, c.Pause, c.Notice)
	}

	if *ledsOn {
		startLEDs(st)
	}

	// Collecte lente (disques, onduleur, RAID…) hors de la boucle de sécurité: un outil bloqué ne la retarde pas.
	scrub := &scrubWatch{}
	go func() {
		lastRecErr := ""
		for {
			safely("collecte NAS", func() {
				n := collectNAS()
				st.setNAS(n)
				checkPower(n)
				if note := scrub.observe(n, time.Now()); note.Kind != "" {
					logf("%s", note.Text)
					events.add(eventRec{At: time.Now(), Kind: "event", Key: "scrub", Title: note.Msg.Subject, Text: note.Text, Gravity: "basse"})
					if disp != nil {
						disp.post(note.Msg)
					} else if nt != nil {
						nt.post(note.Msg)
					}
				}
				if rec != nil {
					now := time.Now()
					in, _ := st.view(now)
					l := lim
					l.MinRPM = *minRPM
					_, maint := maintenanceUntil(*maintFile, now)
					err := rec.write(now, recordRow(n, true, in.Devs, in.DevsOK, l, *nasCh, *qnapCh, maint))
					switch {
					case err != nil && err.Error() != lastRecErr:
						lastRecErr = err.Error()
						log.Printf("ATTENTION: enregistrement des températures impossible: %v", err)
					case err == nil && lastRecErr != "":
						lastRecErr = ""
						log.Printf("Enregistrement des températures rétabli")
					}
				}
			})
			time.Sleep(*collectEvery)
		}
	}()
	if *listenAddr != "" {
		w := &webCtx{st: st, al: al, hist: hist, nt: nt, ai: an, ev: events, started: m.started, dry: *dry, nasCh: *nasCh, qnapCh: *qnapCh, recDir: *recordDir}
		if pw := readEnvFile(*secretsPath)["CONTROL_PASSWORD"]; pw != "" {
			var restart func() error
			if *restartCmd != "" {
				cmd := *restartCmd
				restart = func() error { return detachedRun(cmd) }
			}
			w.ctl = newControlCtx(pw, testBeep, beepCfg, restart, events, *dry)
			if w.ctl.enabled() {
				log.Printf("Page de commandes: http://%s/commandes (mot de passe requis)", *listenAddr)
			} else {
				log.Printf("ATTENTION: CONTROL_PASSWORD trop court (8 caractères au moins): page de commandes désactivée")
			}
		} else {
			log.Printf("Page de commandes désactivée (CONTROL_PASSWORD absent de secrets.env)")
		}
		go safely("page d'état", func() { w.serve(*listenAddr) })
		log.Printf("Page d'état: http://%s/", *listenAddr)
	}
	nt.post(startupMessage(readUptime(), *dry, pageURL()))
	if stop, until, active := guardUntil(*autoStopFile, *autoStopGuard, time.Now()); active {
		m.fanGuardUntil = until
		log.Printf("Dernier arrêt automatique le %s (%s): arrêt par la ventilation suspendu jusqu'à %s",
			stop.At.Format("02/01 15:04"), stop.Reason, until.Format("02/01 15:04"))
		nt.post(autoStopMessage(stop, until, *dry, pageURL()))
	}

	lastOK := time.Now()
	lastBeat := time.Time{}
	lastHist := time.Time{}
	wasMaint := false
	var lastRemind time.Time
	var hb beater
	for {
		now := time.Now()
		if err := hb.tick(*heartbeatFile, now); err != nil {
			m.warnOnce("heartbeat", fmt.Sprintf("signe de vie non écrit (%v): le superviseur croira la surveillance arrêtée", err))
		} else {
			m.clear("heartbeat")
		}
		if t, ok := cpuTemp(); ok {
			if r := m.cpuCheck(t, now); r != "" {
				shutdown(r)
			}
		}
		devs, err := fetch()
		if err != nil {
			st.setDevs(nil, false, now)
			m.warnOnce("apiFirst", fmt.Sprintf("API OpenLinkHub injoignable (%v)", err))
			if now.Sub(lastOK) >= *unreachable {
				m.warnOnce("api", fmt.Sprintf("API OpenLinkHub injoignable depuis %s (%v); le Hub garde ses réglages mémorisés", now.Sub(lastOK).Round(time.Second), err))
			}
		} else {
			st.setDevs(devs, true, now)
			if m.warned["api"] {
				log.Printf("API OpenLinkHub de nouveau joignable")
			}
			m.clear("api")
			if m.warned["apiFirst"] {
				log.Printf("API OpenLinkHub joignable")
				m.clear("apiFirst")
			}
			lastOK = now
			if reason := m.evaluate(devs, now); reason != "" {
				if m.fanStop && !*dry {
					// Marqueur écrit AVANT l'arrêt: au redémarrage, la garde contre la boucle d'arrêts s'applique.
					if err := writeAutoStop(*autoStopFile, now, reason); err != nil {
						log.Printf("ATTENTION: marqueur d'arrêt automatique non écrit: %v", err)
					}
				}
				shutdown(reason)
			}
			if now.Sub(lastBeat) >= 5*time.Minute {
				lastBeat = now
				log.Printf("OK\n%s", summary(devs))
			}
		}

		// Couche NAS: anomalies à notifier, arrêt thermique des disques et de la carte mère.
		safely("évaluation NAS", func() {
			in, _ := st.view(now)
			in.Enforce = now.Sub(m.started) >= *grace
			in.NasCh, in.QnapCh, in.UnreachAt = *nasCh, *qnapCh, *unreachable
			in.LostLimit = *fanLostFor
			until, maintOn := maintenanceUntil(*maintFile, now)
			in.Maintenance = maintOn
			if maintOn != wasMaint {
				wasMaint = maintOn
				lastRemind = now
				nt.post(maintenanceMessage(maintOn, until, *dry, pageURL()))
			} else if maintOn && *maintRemind > 0 && now.Sub(lastRemind) >= *maintRemind {
				// Rappel: la protection ventilation est suspendue, il ne faut pas l'oublier.
				lastRemind = now
				nt.post(maintenanceReminder(until, *dry, pageURL()))
			}
			fs := assess(in, lim)
			st.setFindings(fs)
			al.reconcile(now, fs)
			if hc != nil {
				if r := hc.step(in, now, m.vent, m.deadSince); r != "" {
					if now.Before(m.fanGuardUntil) {
						m.warnOnce("hueguard", fmt.Sprintf("%s: pas de nouvel arrêt avant %s (protection contre la boucle d'arrêts), la température des disques reste surveillée", r, m.fanGuardUntil.Format("15:04")))
					} else {
						if !*dry {
							// Marqueur écrit AVANT l'arrêt: au redémarrage, la garde contre la boucle d'arrêts s'applique.
							if err := writeAutoStop(*autoStopFile, now, r); err != nil {
								log.Printf("ATTENTION: marqueur d'arrêt automatique non écrit: %v", err)
							}
						}
						shutdown(r)
					}
				}
			}
			if in.HaveNAS && in.NASAge <= lim.StaleAfter {
				if r := m.nasCheck(in.NAS, now); r != "" {
					shutdown(r)
				}
			}
			if in.HaveNAS && now.Sub(lastHist) >= 5*time.Minute {
				lastHist = now
				hist.add(now, metrics(in, *nasCh, *qnapCh))
			}
			if in.HaveNAS && dig.due(now) {
				go safely("résumé", func() { disp.digest(now) })
			}
		})
		time.Sleep(*interval)
	}
}

// describeNAS décrit un relevé des capteurs du NAS en texte (option -sensors).
func describeNAS(s nasSnap) string {
	var b strings.Builder
	if s.CPUOK {
		fmt.Fprintf(&b, "  processeur: %.1f °C\n", s.CPU)
	}
	for _, k := range sortedKeys(s.BoardTemp) {
		fmt.Fprintf(&b, "  carte mère %s: %.1f °C\n", k, s.BoardTemp[k])
	}
	for n := 1; n <= 6; n++ {
		if r, ok := s.NASFans[n]; ok {
			fmt.Fprintf(&b, "  ventilateur interne n°%d: %d tr/min\n", n, r)
		}
	}
	for _, d := range s.Disks {
		switch {
		case d.Standby:
			fmt.Fprintf(&b, "  disque %s: en veille\n", d.Name)
		default:
			fmt.Fprintf(&b, "  disque %s: %.0f °C, SMART %s, réalloués %d, en attente %d, erreurs non corrigées %d\n",
				d.Name, d.Temp, orDash(d.Health), d.Reallocated, d.Pending, d.Reported)
		}
	}
	if s.UPS.Present {
		fmt.Fprintf(&b, "  onduleur: %s, charge %.0f %%, autonomie %.0f min\n", s.UPS.Status, s.UPS.Charge, s.UPS.RuntimeS/60)
	}
	for _, r := range s.RAID {
		fmt.Fprintf(&b, "  RAID %s (%s): %d/%d %s\n", r.Name, r.Level, r.Up, r.Total, r.Status)
	}
	if s.VolOK {
		fmt.Fprintf(&b, "  volume 1: %.0f %% utilisé\n", s.VolUsed)
	}
	fmt.Fprintf(&b, "  allumé depuis: %s\n", fmtDur(s.Uptime))
	return b.String()
}

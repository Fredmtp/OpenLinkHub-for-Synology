package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---- état partagé entre la boucle de sécurité, la collecte lente et la page web ----

type shared struct {
	mu       sync.RWMutex
	nas      nasSnap
	haveNAS  bool
	devs     []device
	devsOK   bool
	devsAt   time.Time
	apiDown  time.Time // début de l'indisponibilité de l'API (zéro si joignable)
	findings []finding
	baseline map[string]diskBase
	// probeSeen: dernière valeur lue de chaque sonde et depuis quand elle est identique (détection de sonde figée).
	probeSeen map[int]probeValue
	qnap      qnapTrack // baie QNAP: absence et erreurs USB
	hue       hueView   // prise Hue des ventilateurs (état lu sur le Bridge, dernier ordre)
	// fanSeen: dernière vitesse lue de chaque ventilateur du meuble et depuis quand elle est identique (Hub gelé).
	fanSeen map[int]probeValue
}

// trackFans note, pour chaque ventilateur qui tourne, depuis quand sa vitesse est exactement la même. Un Hub privé
// de courant n'efface pas sa liste: OpenLinkHub continue d'afficher les dernières valeurs (nuit du 12/11/2026:
// 1 103 tr/min et 30,6 °C figés de 0 h 15 à 1 h 10 alors que les ventilateurs étaient arrêtés). Sur les relevés
// sains, la plus longue suite de vitesses identiques sur tous les ventilateurs est de 3 minutes.
// L'appelant tient le verrou.
func (s *shared) trackFans(devs []device, now time.Time) {
	if s.fanSeen == nil {
		s.fanSeen = map[int]probeValue{}
	}
	seen := map[int]bool{}
	for _, d := range devs {
		if !isFan(d) || d.RPM <= 0 {
			continue // un ventilateur à l'arrêt est déjà traité par la règle de ventilation perdue
		}
		seen[d.ChannelID] = true
		if p, had := s.fanSeen[d.ChannelID]; !had || p.val != float64(d.RPM) {
			s.fanSeen[d.ChannelID] = probeValue{val: float64(d.RPM), since: now}
		}
	}
	for ch := range s.fanSeen {
		if !seen[ch] {
			delete(s.fanSeen, ch)
		}
	}
}

// fanStill donne depuis combien de temps AUCUN ventilateur n'a changé de vitesse (0 si moins de deux ventilateurs
// tournent: une vitesse qui ne bouge pas n'est pas un signe suffisant avec un seul).
func (s *shared) fanStill(now time.Time) time.Duration {
	if len(s.fanSeen) < 2 {
		return 0
	}
	still := time.Duration(-1)
	for _, p := range s.fanSeen {
		if d := now.Sub(p.since); still < 0 || d < still {
			still = d
		}
	}
	return still
}

// setHue enregistre la dernière lecture de la prise Hue (enabled = un Bridge est configuré).
func (s *shared) setHue(enabled bool, st plugState) {
	s.mu.Lock()
	s.hue.Enabled, s.hue.State = enabled, st
	s.mu.Unlock()
}

// setHueOrder enregistre le dernier ordre donné à la prise.
func (s *shared) setHueOrder(at time.Time, kind string) {
	s.mu.Lock()
	s.hue.LastOrder, s.hue.LastKind = at, kind
	s.mu.Unlock()
}

type probeValue struct {
	val   float64
	since time.Time
}

func (s *shared) setNAS(n nasSnap) {
	s.mu.Lock()
	s.nas, s.haveNAS = n, true
	s.qnap.update(n.Qnap, n.At)
	if s.baseline == nil {
		s.baseline = map[string]diskBase{}
	}
	for _, d := range n.Disks {
		if d.Standby {
			continue
		}
		b, ok := s.baseline[d.Name]
		switch {
		case !ok:
			s.baseline[d.Name] = diskBase{Reported: d.Reported, Reallocated: d.Reallocated, Pending: d.Pending, At: n.At}
		case d.Reported < b.Reported || d.Reallocated < b.Reallocated || d.Pending < b.Pending:
			// Compteurs en baisse: disque remplacé (ou secteurs en attente résolus). Le repère
			// repart du relevé actuel, sinon une dégradation du nouveau disque passerait inaperçue.
			s.baseline[d.Name] = diskBase{Reported: d.Reported, Reallocated: d.Reallocated, Pending: d.Pending, At: n.At}
		}
	}
	s.mu.Unlock()
}

func (s *shared) setDevs(d []device, ok bool, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devsOK = ok
	if ok {
		s.devs, s.devsAt, s.apiDown = d, now, time.Time{}
		s.trackProbes(d, now)
		s.trackFans(d, now)
	} else if s.apiDown.IsZero() {
		s.apiDown = now
	}
}

// trackProbes note, pour chaque sonde lisible, depuis quand sa valeur est exactement la même. Une sonde
// illisible (0 °C ou hors plage) est oubliée: c'est l'alerte « sonde absente ou illisible » qui la couvre.
// L'appelant tient le verrou.
func (s *shared) trackProbes(devs []device, now time.Time) {
	if s.probeSeen == nil {
		s.probeSeen = map[int]probeValue{}
	}
	seen := map[int]bool{}
	for _, d := range devs {
		if !d.IsTemperatureProbe || d.Temperature <= 0 || d.Temperature > 120 {
			continue
		}
		seen[d.ChannelID] = true
		if p, had := s.probeSeen[d.ChannelID]; !had || p.val != d.Temperature {
			s.probeSeen[d.ChannelID] = probeValue{val: d.Temperature, since: now}
		}
	}
	for ch := range s.probeSeen {
		if !seen[ch] {
			delete(s.probeSeen, ch)
		}
	}
}

// view renvoie une copie cohérente des mesures pour l'évaluation et l'affichage.
func (s *shared) view(now time.Time) (in assessInput, findings []finding) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	in = assessInput{NAS: s.nas, HaveNAS: s.haveNAS, Devs: s.devs, DevsOK: s.devsOK, Baseline: s.baseline}
	in.Qnap = s.qnap.view(now)
	in.Hue = s.hue
	in.Hue.Now = now
	in.FanStill = s.fanStill(now)
	if len(s.probeSeen) > 0 {
		in.ProbeStill = make(map[int]time.Duration, len(s.probeSeen))
		for ch, p := range s.probeSeen {
			in.ProbeStill[ch] = now.Sub(p.since)
		}
	}
	if s.haveNAS {
		in.NASAge = now.Sub(s.nas.At)
	}
	if !s.apiDown.IsZero() {
		in.APIDownFor = now.Sub(s.apiDown)
	}
	return in, append([]finding(nil), s.findings...)
}

func (s *shared) setFindings(f []finding) {
	s.mu.Lock()
	s.findings = f
	s.mu.Unlock()
}

// ---- suivi des alertes ----

type alertState struct {
	finding
	since       time.Time // première apparition
	sentLevel   string    // niveau déjà notifié ("" = pas encore notifié)
	lastSent    time.Time
	absentSince time.Time // zéro tant que le constat est présent
}

type alerter struct {
	mu      sync.Mutex
	active  map[string]*alertState
	post    func(message)
	confirm time.Duration // un constat doit durer au moins ceci avant d'être notifié
	recover time.Duration // et rester absent au moins ceci avant d'annoncer le retour à la normale
	remindW time.Duration // rappel d'une alerte persistante
	remindC time.Duration // rappel d'une alerte critique persistante
}

func newAlerter(post func(message)) *alerter {
	return &alerter{active: map[string]*alertState{}, post: post,
		confirm: time.Minute, recover: 2 * time.Minute, remindW: 6 * time.Hour, remindC: time.Hour}
}

func levelPrio(l string) int {
	if l == levelCrit {
		return 5
	}
	return 4
}

// reconcile compare les constats du moment à ceux déjà connus et envoie UN message groupé
// pour tout ce qui est nouveau, aggravé, à rappeler ou rétabli.
func (a *alerter) reconcile(now time.Time, fs []finding) {
	a.mu.Lock()
	defer a.mu.Unlock()

	seen := map[string]bool{}
	var fresh, remind, back []string
	var items []alertItem // même contenu, structuré pour le répartiteur
	prio, tags := 0, []string{}

	for _, f := range fs {
		seen[f.Key] = true
		st := a.active[f.Key]
		if st == nil {
			st = &alertState{finding: f, since: now}
			a.active[f.Key] = st
		}
		st.absentSince = time.Time{}
		st.finding = f
		if now.Sub(st.since) < a.confirm {
			continue
		}
		switch {
		case st.sentLevel == "" || (f.Level == levelCrit && st.sentLevel != levelCrit):
			fresh = append(fresh, fmt.Sprintf("%s: %s", strings.ToUpper(f.Level), f.Text))
			items = append(items, alertItem{Kind: "fresh", F: f})
			st.sentLevel, st.lastSent = f.Level, now
			if levelPrio(f.Level) > prio {
				prio = levelPrio(f.Level)
			}
		default:
			every := a.remindW
			if f.Level == levelCrit {
				every = a.remindC
			}
			// RAID dégradé ou en reconstruction: état voulu et long (heures, jours), rappel toutes
			// les 6 h. L'absence totale de redondance (clé « -severe ») garde le rythme normal.
			if strings.HasPrefix(f.Key, "raid-") && !strings.HasSuffix(f.Key, "-severe") {
				every = 6 * time.Hour
			}
			// Défauts de surface d'un disque: état durable, un rappel par jour suffit.
			if strings.HasPrefix(f.Key, "disk-bad-") || strings.HasPrefix(f.Key, "disk-realloc-") || strings.HasPrefix(f.Key, "disk-errors-") {
				every = 24 * time.Hour
			}
			if now.Sub(st.lastSent) >= every {
				remind = append(remind, fmt.Sprintf("%s (toujours en cours depuis %s): %s",
					strings.ToUpper(f.Level), now.Sub(st.since).Round(time.Minute), f.Text))
				st.lastSent = now
				items = append(items, alertItem{Kind: "remind", F: f})
				if prio < 3 {
					prio = 3
				}
			}
		}
	}
	for k, st := range a.active {
		if seen[k] {
			continue
		}
		if st.absentSince.IsZero() {
			st.absentSince = now
		}
		if now.Sub(st.absentSince) < a.recover {
			continue
		}
		if st.sentLevel != "" {
			back = append(back, "RÉTABLI: "+st.Text)
			items = append(items, alertItem{Kind: "back", F: st.finding})
		}
		delete(a.active, k)
	}
	if len(fresh)+len(remind)+len(back) == 0 {
		return
	}

	var body, short []string
	subject := []string{}
	if len(fresh) > 0 {
		subject = append(subject, plural(len(fresh), "alerte", "alertes"))
		body = append(body, "=== NOUVEAU ===")
		body = append(body, fresh...)
		short = append(short, fresh...)
	}
	if len(remind) > 0 {
		subject = append(subject, plural(len(remind), "rappel", "rappels"))
		body = append(body, "", "=== TOUJOURS EN COURS ===")
		body = append(body, remind...)
		short = append(short, remind...)
	}
	if len(back) > 0 {
		subject = append(subject, plural(len(back), "rétablissement", "rétablissements"))
		body = append(body, "", "=== RETOUR À LA NORMALE ===")
		body = append(body, back...)
		short = append(short, back...)
	}
	switch {
	case prio >= 5:
		tags = []string{"rotating_light"}
	case prio >= 3:
		tags = []string{"warning"}
	default:
		prio, tags = 2, []string{"white_check_mark"}
	}
	if len(short) > 4 {
		short = append(short[:4], fmt.Sprintf("… et %d autre(s)", len(short)-4))
	}
	a.post(message{
		Items:    items,
		Subject:  "[NAS meuble] " + strings.Join(subject, ", "),
		Body:     strings.Join(body, "\n") + "\n\nÉtat complet: voir la page d'état du NAS.\n",
		Short:    strings.Join(short, "\n"),
		Priority: prio, Tags: tags,
	})
}

func plural(n int, one, many string) string {
	if n <= 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// activeKeys liste les constats actuellement ouverts (pour la page d'état et les tests).
func (a *alerter) activeKeys() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var k []string
	for key := range a.active {
		k = append(k, key)
	}
	sort.Strings(k)
	return k
}

// ---- historique sur 24 h ----

type sample struct {
	At   time.Time
	Vals map[string]float64
}

type history struct {
	mu      sync.Mutex
	samples []sample
	keep    time.Duration
}

func newHistory() *history { return &history{keep: 24 * time.Hour} }

func (h *history) add(now time.Time, vals map[string]float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.samples = append(h.samples, sample{At: now, Vals: vals})
	cut := 0
	for cut < len(h.samples) && now.Sub(h.samples[cut].At) > h.keep {
		cut++
	}
	h.samples = h.samples[cut:]
}

type span struct {
	Min, Max, Last float64
	OK             bool
}

func (h *history) span(key string) span {
	h.mu.Lock()
	defer h.mu.Unlock()
	var s span
	for _, sm := range h.samples {
		v, ok := sm.Vals[key]
		if !ok {
			continue
		}
		if !s.OK || v < s.Min {
			s.Min = v
		}
		if !s.OK || v > s.Max {
			s.Max = v
		}
		s.Last, s.OK = v, true
	}
	return s
}

// metrics résume l'état courant en quelques valeurs suivies dans l'historique.
func metrics(in assessInput, nasCh, qnapCh int) map[string]float64 {
	m := map[string]float64{}
	if in.HaveNAS {
		if _, t, ok := in.NAS.maxDiskTemp(); ok {
			m["disque max"] = t
		}
		if in.NAS.CPUOK {
			m["processeur"] = in.NAS.CPU
		}
		if _, t, ok := in.NAS.maxBoardTemp(); ok {
			m["carte mère max"] = t
		}
		sum, n := 0, 0
		for _, r := range in.NAS.NASFans {
			if r > 0 {
				sum += r
				n++
			}
		}
		if n > 0 {
			m["ventilateurs NAS (tr/min)"] = float64(sum) / float64(n)
		}
	}
	if in.DevsOK {
		sum, n := 0, 0
		for _, d := range in.Devs {
			switch {
			case isFan(d):
				sum += d.RPM
				n++
			case d.IsTemperatureProbe && d.ChannelID == nasCh && d.Temperature > 0:
				m["meuble: sonde NAS"] = d.Temperature
			case d.IsTemperatureProbe && d.ChannelID == qnapCh && d.Temperature > 0:
				m["meuble: sonde QNAP"] = d.Temperature
			}
		}
		if n > 0 {
			m["ventilateurs meuble (tr/min)"] = float64(sum) / float64(n)
		}
	}
	return m
}

var metricOrder = []string{"meuble: sonde NAS", "meuble: sonde QNAP", "ventilateurs meuble (tr/min)",
	"processeur", "carte mère max", "ventilateurs NAS (tr/min)", "disque max"}

func (h *history) lines() []string {
	var out []string
	for _, k := range metricOrder {
		s := h.span(k)
		if !s.OK {
			continue
		}
		unit := " °C"
		if strings.Contains(k, "tr/min") {
			unit = ""
		}
		out = append(out, fmt.Sprintf("  %-30s actuel %.0f%s · min %.0f · max %.0f (24 h)", k, s.Last, unit, s.Min, s.Max))
	}
	return out
}

// ---- résumé périodique ----

type digester struct {
	hours []int
	sent  map[string]bool // « 2026-10-26@13 » déjà envoyé
}

func newDigester(hours []int) *digester { return &digester{hours: hours, sent: map[string]bool{}} }

// due renvoie vrai une seule fois par jour et par heure programmée, dès que l'heure est passée.
func (d *digester) due(now time.Time) bool {
	for _, h := range d.hours {
		if now.Hour() == h {
			key := fmt.Sprintf("%s@%02d", now.Format("2006-01-02"), h)
			if !d.sent[key] {
				d.sent[key] = true
				return true
			}
		}
	}
	return false
}

func parseHours(s string) []int {
	var out []int
	for _, p := range strings.Split(s, ",") {
		var h int
		if _, err := fmt.Sscanf(strings.TrimSpace(p), "%d", &h); err == nil && h >= 0 && h < 24 {
			out = append(out, h)
		}
	}
	return out
}

// digestMessage rédige le résumé périodique: court sur le téléphone, détaillé par mail.
func digestMessage(now time.Time, in assessInput, fs []finding, h *history, dry bool, nasCh, qnapCh int) message {
	var body []string
	mode := ""
	if dry {
		mode = " (surveillance en mode à blanc: n'éteint pas le NAS)"
	}
	body = append(body, fmt.Sprintf("Résumé du %s à %s%s", now.Format("02/01/2006"), now.Format("15:04"), mode), "")
	if len(fs) == 0 {
		body = append(body, "Aucune anomalie.")
	} else {
		body = append(body, plural(len(fs), "anomalie en cours:", "anomalies en cours:"))
		for _, f := range fs {
			body = append(body, fmt.Sprintf("  - %s: %s", strings.ToUpper(f.Level), f.Text))
		}
	}
	body = append(body, "", "Mesures:")
	body = append(body, h.lines()...)
	if in.HaveNAS {
		s := in.NAS
		body = append(body, "", "Disques:")
		for _, d := range s.Disks {
			switch {
			case d.Standby:
				body = append(body, fmt.Sprintf("  %s: en veille", d.Name))
			case d.TempOK:
				body = append(body, fmt.Sprintf("  %s: %.0f °C, SMART %s", d.Name, d.Temp, orDash(d.Health)))
			}
		}
		if s.UPS.Present {
			body = append(body, "", fmt.Sprintf("Onduleur: %s, charge %.0f %%, autonomie %.0f min, charge électrique %.0f %%",
				s.UPS.Status, s.UPS.Charge, s.UPS.RuntimeS/60, s.UPS.LoadPct))
		}
		for _, r := range s.RAID {
			if r.Level == "raid5" || r.Level == "raid6" || r.Level == "raid1" || r.Level == "raid10" {
				body = append(body, fmt.Sprintf("RAID %s (%s): %d/%d, %s", r.Name, r.Level, r.Up, r.Total, r.Status))
			}
		}
		if s.VolOK {
			body = append(body, fmt.Sprintf("Volume 1: %.0f %% utilisé", s.VolUsed))
		}
		body = append(body, fmt.Sprintf("Fonctionne depuis: %s", fmtDur(s.Uptime)))
	}

	short := fmt.Sprintf("Résumé %s: ", now.Format("15h04"))
	prio, tags := 2, []string{"bar_chart"}
	if len(fs) == 0 {
		short += "tout est normal."
	} else {
		short += plural(len(fs), "anomalie en cours.", "anomalies en cours.")
		prio, tags = 4, []string{"warning"}
	}
	for _, k := range []string{"meuble: sonde NAS", "disque max"} {
		if s := h.span(k); s.OK {
			short += fmt.Sprintf(" %s %.0f °C.", strings.TrimPrefix(k, "meuble: "), s.Last)
		}
	}
	return message{Subject: fmt.Sprintf("[NAS meuble] résumé de %s", now.Format("15h04")),
		Body: strings.Join(body, "\n") + "\n", Short: short, Priority: prio, Tags: tags}
}

func orDash(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func fmtDur(d time.Duration) string {
	if d >= 24*time.Hour {
		return fmt.Sprintf("%d j %d h", int(d.Hours())/24, int(d.Hours())%24)
	}
	return fmt.Sprintf("%d h %02d min", int(d.Hours()), int(d.Minutes())%60)
}

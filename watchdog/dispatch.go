package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Le répartiteur applique les règles de pertinence:
//   - critique et haute: partent tout de suite (mail détaillé + push court), jamais retenues par l'IA;
//   - moyenne et basse: attendent le prochain résumé (8 h, 13 h, 19 h), sauf si l'IA les juge urgentes;
//   - situations « déjà connues » (fichier -known): jamais poussées, rappelées une fois par semaine;
//   - IA absente ou en panne: texte standard, l'alerte part quand même.

// alertItem est un événement produit par le suivi des alertes (alerts.go) ou par la détection de coupures.
type alertItem struct {
	Kind string // fresh, remind, back, event
	F    finding
}

type dispatcher struct {
	mu          sync.Mutex
	send        func(message)
	ai          aiAnalyzer // nil = pas d'IA
	dry         bool
	pageURL     string
	known       map[string]string // clé d'anomalie -> note de l'utilisateur
	knownPath   string            // fichier relu à chaud (vide = table fixe)
	pending     []card            // cartes moyennes ou basses en attente du prochain résumé
	pendingDisc []card            // écartées par l'IA, pour contrôle
	events      *eventLog         // journal des événements (nil = non conservé)
	state       func() (assessInput, []finding)
	hist        *history
	now         func() time.Time
	firstDigest int
	timeoutNow  time.Duration
	timeoutDig  time.Duration
}

func newDispatcher(send func(message), ai aiAnalyzer, dry bool, pageURL string, known map[string]string,
	state func() (assessInput, []finding), hist *history, firstDigest int) *dispatcher {
	return &dispatcher{send: send, ai: ai, dry: dry, pageURL: pageURL, known: known, state: state, hist: hist,
		now: time.Now, firstDigest: firstDigest, timeoutNow: 20 * time.Second, timeoutDig: 60 * time.Second}
}

// parseKnown lit le fichier des situations connues: une ligne « clé | note », # pour commenter.
func parseKnown(txt string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(txt, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, note, _ := strings.Cut(line, "|")
		if key = strings.TrimSpace(key); key != "" {
			out[key] = strings.TrimSpace(note)
		}
	}
	return out
}

func loadKnown(path string) map[string]string {
	m, _ := readKnown(path)
	return m
}

// readKnown lit le fichier des situations connues; le booléen dit si la lecture a réussi.
func readKnown(path string) (map[string]string, bool) {
	if path == "" {
		return map[string]string{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}, false
	}
	return parseKnown(string(b)), true
}

// knownMap relit le fichier des situations connues à chaque appel, pour qu'une modification soit prise en
// compte sans relancer la surveillance. Si le fichier devient illisible, la dernière table connue est gardée.
func (d *dispatcher) knownMap() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.knownPath != "" {
		if m, ok := readKnown(d.knownPath); ok {
			d.known = m
		}
	}
	return d.known
}

// post reçoit les messages du suivi des alertes. Sans événement structuré (message simple), il passe tel quel.
func (d *dispatcher) post(m message) {
	if len(m.Items) == 0 {
		d.send(m)
		return
	}
	go safely("répartiteur", func() { d.process(m.Items) })
}

func backCard(f finding, in assessInput) card {
	base := describeFinding(f, in)
	return card{Key: f.Key, Gravity: "basse", Where: base.Where, Title: "Rétabli : " + base.Title,
		Summary: "Le problème signalé n'est plus constaté.", Action: "Rien de particulier à faire.",
		Equip: base.Equip, Push: f.Level == levelCrit}
}

func (d *dispatcher) process(items []alertItem) {
	in, _ := d.state()
	knownNow := d.knownMap()
	var cards []card
	for _, it := range items {
		if it.Kind == "fresh" || it.Kind == "remind" {
			if _, isKnown := knownNow[it.F.Key]; isKnown {
				continue // déjà connu: jamais poussé
			}
		}
		var c card
		if it.Kind == "back" {
			c = backCard(it.F, in)
		} else {
			c = describeFinding(it.F, in)
		}
		cards = append(cards, c)
		if it.Kind != "remind" { // un rappel n'est pas un nouvel événement
			d.events.add(eventRec{At: d.now(), Kind: it.Kind, Key: it.F.Key, Title: c.Title, Text: it.F.Text, Gravity: c.Gravity})
		}
	}
	if len(cards) == 0 {
		return
	}
	urgent := false
	for _, c := range cards {
		if c.Push || gravRank(c.Gravity) <= gravRank("haute") {
			urgent = true
		}
	}
	timeout := d.timeoutDig
	if urgent {
		timeout = d.timeoutNow // une alerte urgente n'attend pas l'IA plus de quelques secondes
	}
	cards, discarded, res := d.enrich("alerte", cards, in, timeout)

	var now, later []card
	for _, c := range cards {
		if c.Push || gravRank(c.Gravity) <= gravRank("haute") {
			now = append(now, c)
		} else {
			later = append(later, c)
		}
	}
	d.mu.Lock()
	d.pending = append(d.pending, later...)
	d.pendingDisc = append(d.pendingDisc, discarded...)
	d.mu.Unlock()
	if len(now) > 0 {
		d.sendAlert(now, res)
	}
}

// enrich demande à l'IA de rédiger les cartes. En cas d'échec, les cartes gardent leur texte standard.
func (d *dispatcher) enrich(kind string, cards []card, in assessInput, timeout time.Duration) ([]card, []card, *aiResult) {
	if d.ai == nil || len(cards) == 0 {
		return cards, nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	res, err := d.ai.analyze(ctx, kind, cards, stateLines(in), d.knownLines(), d.recentLines())
	if err != nil {
		logf("ATTENTION: analyse IA indisponible (%v): texte standard", err)
		return cards, nil, nil
	}
	kept, discarded := applyAI(cards, res)
	return kept, discarded, res
}

// recentLines donne à l'IA les événements des dernières 24 h (15 au plus), pour rapprocher des événements espacés.
func (d *dispatcher) recentLines() []string {
	return eventLines(d.events.since(d.now().Add(-24*time.Hour)), 15)
}

func (d *dispatcher) knownLines() []string {
	var out []string
	for k, note := range d.knownMap() {
		out = append(out, fmt.Sprintf("%s: %s", k, note))
	}
	sort.Strings(out)
	return out
}

func (d *dispatcher) opts() mailOpts {
	return mailOpts{Dry: d.dry, AI: d.ai != nil, PageURL: d.pageURL}
}

func anyAnalyzed(cards []card) bool {
	for _, c := range cards {
		if c.Analyzed {
			return true
		}
	}
	return false
}

func (d *dispatcher) sendAlert(cards []card, res *aiResult) {
	o := d.opts()
	o.AI = d.ai != nil
	if len(cards) > 1 && res != nil {
		o.Title = res.Title
	}
	subject, html := renderAlertMail(d.now(), cards, o)
	prio, tags := 3, []string{"information_source"}
	var short []string
	var plain []string
	for _, c := range cards {
		g := gravities[c.Gravity]
		short = append(short, strings.ToUpper(g.Label)+" : "+c.Title)
		plain = append(plain, fmt.Sprintf("[%s] %s\n%s\nÀ faire : %s\n", strings.ToUpper(g.Label), c.Title, c.Summary, c.Action))
		switch c.Gravity {
		case "critique":
			prio, tags = 5, []string{"rotating_light"}
		case "haute":
			if prio < 4 {
				prio, tags = 4, []string{"warning"}
			}
		}
	}
	if len(short) > 4 {
		short = append(short[:4], fmt.Sprintf("… et %d autre(s)", len(short)-4))
	}
	d.send(message{Subject: subject, Body: strings.Join(plain, "\n"), HTML: html, Short: strings.Join(short, "\n"), Priority: prio, Tags: tags})
}

// ---- résumé périodique ----

func stateLines(in assessInput) []string {
	var out []string
	for _, d := range in.Devs {
		switch {
		case d.IsTemperatureProbe && d.Temperature > 0:
			out = append(out, fmt.Sprintf("Sonde du meuble canal %d: %s °C", d.ChannelID, fr1(d.Temperature)))
		}
	}
	if in.DevsOK {
		run, tot := 0, 0
		for _, d := range in.Devs {
			if isFan(d) {
				tot++
				if d.RPM >= 100 {
					run++
				}
			}
		}
		out = append(out, fmt.Sprintf("Ventilateurs du meuble en marche: %d sur %d", run, tot))
	} else {
		out = append(out, "OpenLinkHub injoignable: ventilateurs du meuble non mesurables")
	}
	if !in.HaveNAS {
		return out
	}
	s := in.NAS
	if s.CPUOK {
		out = append(out, fmt.Sprintf("Processeur: %s °C", fr1(s.CPU)))
	}
	if n, t, ok := s.maxBoardTemp(); ok {
		out = append(out, fmt.Sprintf("Carte mère (%s): %s °C", n, fr1(t)))
	}
	if name, t, ok := s.maxDiskTemp(); ok {
		out = append(out, fmt.Sprintf("Disque le plus chaud: disque %s à %.0f °C", diskNum(name), t))
	}
	for _, r := range s.RAID {
		if r.Total >= 4 {
			out = append(out, fmt.Sprintf("RAID %s (%s): %d disques sur %d, état %s", r.Name, r.Level, r.Up, r.Total, r.Status))
		}
	}
	if s.UPS.Present {
		out = append(out, fmt.Sprintf("Onduleur: %s, charge %.0f %%, autonomie %.0f min", s.UPS.Status, s.UPS.Charge, s.UPS.RuntimeS/60))
	}
	out = append(out, "NAS en marche depuis "+fmtDur(s.Uptime))
	return out
}

func spanText(h *history, key, unit string, withRange bool) (string, bool) {
	s := h.span(key)
	if !s.OK {
		return "", false
	}
	if !withRange {
		return fmt.Sprintf("%s%s", frInt(int(s.Last+0.5)), unit), true
	}
	return fmt.Sprintf("%s%s · min %.0f · max %.0f", fr1(s.Last), unit, s.Min, s.Max), true
}

func (d *dispatcher) digestData(in assessInput, fs []finding, cards, disc []card, showKnown bool) digestData {
	dg := digestData{Cards: cards, Discarded: disc, KnownShown: showKnown}
	knownNow := d.knownMap()
	for _, f := range fs {
		if note, ok := knownNow[f.Key]; ok {
			c := describeFinding(f, in)
			dg.Known = append(dg.Known, knownItem{Title: c.Title, Note: note})
		}
	}
	sort.Slice(dg.Known, func(i, j int) bool { return dg.Known[i].Title < dg.Known[j].Title })
	if d.hist != nil {
		add := func(label, key, unit string, rng bool) {
			if t, ok := spanText(d.hist, key, unit, rng); ok {
				dg.Meuble = append(dg.Meuble, measure{Label: label, Value: t})
			}
		}
		add("Sonde du meuble (NAS)", "meuble: sonde NAS", " °C", true)
		add("Sonde du meuble (baie QNAP)", "meuble: sonde QNAP", " °C", true)
		add("Ventilateurs du meuble", "ventilateurs meuble (tr/min)", " tr/min", false)
		add("Ventilateurs du NAS", "ventilateurs NAS (tr/min)", " tr/min", false)
		add("Processeur", "processeur", " °C", true)
		add("Carte mère", "carte mère max", " °C", true)
	}
	if in.HaveNAS {
		s := in.NAS
		disks := append([]diskInfo(nil), s.Disks...)
		sort.Slice(disks, func(i, j int) bool { return disks[i].Name < disks[j].Name })
		for _, dk := range disks {
			t := diskTile(dk, defaultLimits())
			note := map[string]string{"ok": "sain", "warn": "sous surveillance", "crit": "défaut confirmé"}[t.Level]
			if dk.Standby {
				note = "en veille"
			}
			dg.Disks = append(dg.Disks, diskView{Name: t.Title, Temp: t.Value, Level: t.Level, Note: note})
		}
		if s.UPS.Present {
			lvl := "ok"
			if strings.Contains(s.UPS.Status, "OB") || strings.Contains(s.UPS.Status, "LB") {
				lvl = "crit"
			}
			dg.Equipment = append(dg.Equipment, measure{"Onduleur Eaton 5P", fmt.Sprintf("%s · %.0f %% · %.0f min d'autonomie", s.UPS.Status, s.UPS.Charge, s.UPS.RuntimeS/60), lvl})
		}
		var data *raidInfo
		for i := range s.RAID {
			if data == nil || s.RAID[i].Total > data.Total {
				data = &s.RAID[i]
			}
		}
		if data != nil {
			lvl := "ok"
			if data.Status != "ok" {
				lvl = "crit"
			}
			dg.Equipment = append(dg.Equipment, measure{fmt.Sprintf("RAID %s (%d disques)", strings.TrimPrefix(data.Level, "raid"), data.Total),
				fmt.Sprintf("%d sur %d · %s", data.Up, data.Total, map[string]string{"ok": "sain", "degraded": "dégradé", "resync": "resynchronisation"}[data.Status]), lvl})
		}
		if s.VolOK {
			dg.Equipment = append(dg.Equipment, measure{"Volume 1", fmt.Sprintf("%.0f %% utilisé", s.VolUsed), ""})
		}
		dg.Equipment = append(dg.Equipment, measure{"NAS en marche depuis", fmtDur(s.Uptime), ""})
	}
	return dg
}

// digest compose et envoie le résumé. Appelé par la boucle principale aux heures programmées.
func (d *dispatcher) digest(at time.Time) {
	in, fs := d.state()
	d.mu.Lock()
	cards, disc := d.pending, d.pendingDisc
	d.pending, d.pendingDisc = nil, nil
	d.mu.Unlock()

	showKnown := at.Weekday() == time.Monday && at.Hour() == d.firstDigest
	needAI := false
	for _, c := range cards {
		if !c.Analyzed {
			needAI = true
		}
	}
	var res *aiResult
	if needAI {
		var more []card
		cards, more, res = d.enrich("resume", cards, in, d.timeoutDig)
		disc = append(disc, more...)
	}
	dg := d.digestData(in, fs, cards, disc, showKnown)
	dg.Events = d.events.since(at.Add(-24 * time.Hour))

	o := d.opts()
	o.Hour = fmt.Sprintf("%d H", at.Hour())
	switch {
	case res != nil && res.Title != "":
		o.Title, o.Intro = res.Title, res.Intro
	case len(cards) == 0 && len(dg.Known) > 0:
		o.Title = fmt.Sprintf("Tout fonctionne, %d point(s) restent sous surveillance", len(dg.Known))
	}
	subject, html := renderDigestMail(at, dg, o)

	prio, tags := 2, []string{"bar_chart"}
	short := fmt.Sprintf("Résumé de %s : ", strings.ToLower(o.Hour))
	if len(cards) == 0 {
		short += "rien de nouveau."
	} else {
		short += fmt.Sprintf("%d point(s) à connaître.", len(cards))
		prio = 3
	}
	if len(dg.Known) > 0 {
		short += fmt.Sprintf(" %d point(s) déjà connus sous surveillance.", len(dg.Known))
	}
	var plain []string
	for _, c := range cards {
		plain = append(plain, fmt.Sprintf("- %s : %s", c.Title, c.Summary))
	}
	d.send(message{Subject: subject, Body: strings.Join(plain, "\n") + "\n", HTML: html, Short: short, Priority: prio, Tags: tags})
}

// ---- messages à texte fixe (jamais d'IA: ils doivent partir vite et sans dépendance) ----

func fixedMessage(c card, kicker string, dry bool, pageURL string, prio int, tags []string, short string) message {
	subject, html := renderAlertMail(time.Now(), []card{c}, mailOpts{Dry: dry, PageURL: pageURL, Kicker: kicker})
	return message{Subject: subject, Body: c.Title + "\n" + c.Summary + "\n", HTML: html, Short: short, Priority: prio, Tags: tags}
}

func shutdownMessage(reason string, pageURL string) message {
	c := card{Key: "shutdown", Gravity: "critique", Where: "Surveillance", Title: "Arrêt automatique du NAS",
		Summary: "Le NAS va s'éteindre pour se protéger. Raison : " + reason + ".",
		Action:  "Attendez l'extinction complète, vérifiez la ventilation du meuble et la cause avant de le rallumer.",
		Why:     "Les seuils de sécurité sont dépassés: l'arrêt évite une surchauffe du meuble.", Equip: "DS1825+"}
	return fixedMessage(c, "ARRÊT AUTOMATIQUE", false, pageURL, 5, []string{"rotating_light"}, "ARRÊT automatique du NAS : "+reason)
}

func startupMessage(uptime time.Duration, dry bool, pageURL string) message {
	mode := "arrêt automatique ACTIF"
	if dry {
		mode = "mode à blanc (n'éteint pas le NAS)"
	}
	c := card{Key: "startup", Gravity: "basse", Where: "Surveillance", Title: "Surveillance démarrée",
		Summary: fmt.Sprintf("La surveillance du NAS a démarré. Le NAS est allumé depuis %s. Mode : %s.", fmtDur(uptime), mode),
		Action:  "Rien de particulier à faire.", Equip: "DS1825+"}
	return fixedMessage(c, "SURVEILLANCE", dry, pageURL, 2, []string{"white_check_mark"},
		fmt.Sprintf("Surveillance démarrée (NAS allumé depuis %s, %s).", fmtDur(uptime), mode))
}

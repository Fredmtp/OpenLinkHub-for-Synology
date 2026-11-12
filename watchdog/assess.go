package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Seuils d'alerte (notification) et d'arrêt. Les seuils d'arrêt thermiques des disques et de la
// carte mère sont volontairement plus élevés que ceux d'alerte: on prévient d'abord, on éteint ensuite.
type limits struct {
	DiskWarn, DiskCrit   float64 // alerte
	DiskShutdown         float64 // arrêt si maintenu (-disk-hold)
	DiskInstant          float64 // arrêt sur deux mesures
	CPUWarn, CPUCrit     float64
	BoardWarn            float64
	BoardShutdown        float64
	BoardInstant         float64
	ProbeWarn, ProbeCrit float64 // sondes du meuble (Commander DUO)
	// ProbeFrozen: durée de valeur STRICTEMENT identique au bout de laquelle une sonde est jugée figée
	// (0 = contrôle désactivé). Les 29/10 – 02/11/2026 deux sondes sont restées figées 3 jours et 14 h
	// sans alerte; sur des relevés sains la plus longue plage identique est d'environ 2 h.
	ProbeFrozen time.Duration
	// Baie QNAP: absence au bout de laquelle on alerte, et durée pendant laquelle de nouvelles erreurs du noyau restent signalées.
	QnapAbsent, QnapErrorsFor time.Duration
	// HueUnreachable: durée sans réponse du Bridge Hue au bout de laquelle on alerte (la prise n'est plus surveillée).
	HueUnreachable time.Duration
	// HubStale: durée pendant laquelle aucun ventilateur ne change de vitesse (relevés sains: 3 minutes au plus)
	// au bout de laquelle OpenLinkHub est jugé figé (0 = contrôle désactivé).
	HubStale         time.Duration
	VolWarn, VolCrit float64
	NASFans          []int // ventilateurs internes du NAS attendus (adt7490)
	CorsairFans      int   // nombre de ventilateurs Corsair attendus
	ExtractionFans   []int // canaux des ventilateurs d'extraction du meuble (l'admission n'en fait pas partie)
	ExtractionStop   int   // nombre d'extracteurs arrêtés ou absents qui rend la ventilation « dégradée » (alerte; 0 = règle désactivée)
	IntakeFan        int   // canal du ventilateur d'admission (0 = non suivi)
	MinRPM           int
	StaleAfter       time.Duration
}

func defaultLimits() limits {
	return limits{
		DiskWarn: 40, DiskCrit: 43, DiskShutdown: 45, DiskInstant: 47,
		CPUWarn: 75, CPUCrit: 85,
		BoardWarn: 55, BoardShutdown: 65, BoardInstant: 75,
		ProbeWarn: 35, ProbeCrit: 42, ProbeFrozen: 4 * time.Hour, QnapAbsent: 5 * time.Minute, QnapErrorsFor: 30 * time.Minute,
		HueUnreachable: 10 * time.Minute, HubStale: 10 * time.Minute,
		VolWarn: 90, VolCrit: 95,
		NASFans: []int{1, 2}, CorsairFans: 4, ExtractionFans: []int{1, 2, 3}, ExtractionStop: 2, IntakeFan: 15, MinRPM: 100,
		StaleAfter: 5 * time.Minute,
	}
}

// extractionDown liste les ventilateurs d'extraction considérés arrêtés: listés sous minRPM, ou absents
// de la liste d'OpenLinkHub (on ne peut alors pas savoir s'ils tournent). any est faux quand aucun
// ventilateur n'est listé du tout: la mesure est impossible, ce qui n'est pas une panne prouvée.
func extractionDown(devs []device, channels []int, minRPM int) (down []int, any bool) {
	rpm := map[int]int{}
	for _, d := range devs {
		if isFan(d) {
			rpm[d.ChannelID] = d.RPM
			any = true
		}
	}
	if !any {
		return nil, false
	}
	for _, ch := range channels {
		if r, ok := rpm[ch]; !ok || r < minRPM {
			down = append(down, ch)
		}
	}
	return down, true
}

// ventState classe la ventilation du meuble (décision du 28/10/2026). Seule la perte totale fait
// éteindre le NAS, après un délai (-fan-lost-for); les autres états alertent, et l'arrêt ne vient
// alors que de la température des disques.
type ventState int

const (
	ventOK       ventState = iota
	ventPartial            // un extracteur, l'admission ou un autre ventilateur arrêté
	ventDegraded           // au moins lim.ExtractionStop extracteurs arrêtés ou absents, mais pas tous
	ventLost               // plus aucun ventilateur en rotation, ou tous les extracteurs arrêtés ou absents
)

func (v ventState) String() string {
	switch v {
	case ventPartial:
		return "partielle"
	case ventDegraded:
		return "dégradée"
	case ventLost:
		return "perdue"
	}
	return "normale"
}

// ventilationState renvoie l'état de la ventilation et la liste des extracteurs arrêtés ou absents.
// ok est faux quand aucun ventilateur n'est listé: la mesure est impossible, pas de verdict.
func ventilationState(devs []device, lim limits) (st ventState, down []int, ok bool) {
	down, any := extractionDown(devs, lim.ExtractionFans, lim.MinRPM)
	if !any {
		return ventOK, nil, false
	}
	rpm := map[int]int{}
	running, below := 0, false
	for _, d := range devs {
		if !isFan(d) {
			continue
		}
		rpm[d.ChannelID] = d.RPM
		if d.RPM >= lim.MinRPM {
			running++
		} else {
			below = true
		}
	}
	intakeDown := false
	if lim.IntakeFan > 0 {
		r, listed := rpm[lim.IntakeFan]
		intakeDown = !listed || r < lim.MinRPM
	}
	switch {
	case running == 0 || (len(lim.ExtractionFans) > 0 && len(down) == len(lim.ExtractionFans)):
		return ventLost, down, true
	case lim.ExtractionStop > 0 && len(down) >= lim.ExtractionStop:
		return ventDegraded, down, true
	case len(down) > 0 || intakeDown || below:
		return ventPartial, down, true
	}
	return ventOK, down, true
}

func joinChannels(ch []int) string {
	s := make([]string, len(ch))
	for i, c := range ch {
		s[i] = fmt.Sprint(c)
	}
	return strings.Join(s, ", ")
}

const (
	levelWarn = "warn"
	levelCrit = "crit"
)

// finding est une anomalie en cours. La clé est stable: tant qu'elle est présente, c'est la même alerte.
type finding struct {
	Key   string
	Level string
	Text  string
}

// diskBase est le premier relevé de compteurs de défauts d'un disque, pour détecter une aggravation.
type diskBase struct {
	Reported, Reallocated, Pending int
	At                             time.Time
}

type assessInput struct {
	Baseline   map[string]diskBase
	NAS        nasSnap
	NASAge     time.Duration
	HaveNAS    bool
	Devs       []device
	DevsOK     bool          // la dernière lecture de l'API OpenLinkHub a réussi
	APIDownFor time.Duration // depuis quand l'API est injoignable
	Enforce    bool          // fin du délai de grâce
	NasCh      int
	QnapCh     int
	UnreachAt  time.Duration
	// ProbeStill: depuis combien de temps chaque sonde (par canal) affiche exactement la même valeur.
	ProbeStill map[int]time.Duration
	Qnap       qnapState // baie QNAP (disparition, erreurs USB)
	Hue        hueView   // prise Hue des ventilateurs (état lu sur le Bridge)
	// FanStill: depuis combien de temps aucun ventilateur du meuble n'a changé de vitesse (Hub gelé ou sans courant).
	FanStill time.Duration
	// Maintenance: l'arrêt « ventilation » est en veille (voir maintenance.go); les alertes de
	// ventilateurs sont alors tues, les seuils thermiques restent actifs.
	Maintenance bool
	// LostLimit est le délai de perte totale de la ventilation au bout duquel le NAS est éteint
	// (-fan-lost-for); il figure dans le texte de l'alerte.
	LostLimit time.Duration
}

func fmtT(t float64) string { return fmt.Sprintf("%.0f °C", t) }

// fmtLimit écrit un délai court en clair (« 15 min », « 90 s »).
func fmtLimit(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%d s", int(d.Seconds()))
}

// assess dresse la liste des anomalies à signaler. Fonction pure: aucune lecture ni envoi.
func assess(in assessInput, lim limits) []finding {
	var out []finding
	add := func(key, level, format string, a ...any) {
		out = append(out, finding{Key: key, Level: level, Text: fmt.Sprintf(format, a...)})
	}

	if in.HaveNAS {
		s := in.NAS
		if in.NASAge > lim.StaleAfter {
			add("nas-stale", levelWarn, "mesures du NAS figées depuis %s", in.NASAge.Round(time.Second))
		}
		for _, d := range s.Disks {
			if d.Standby {
				continue
			}
			if d.TempOK {
				switch {
				case d.Temp >= lim.DiskCrit:
					add("disk-temp-"+d.Name, levelCrit, "disque %s à %s (critique dès %s)", d.Name, fmtT(d.Temp), fmtT(lim.DiskCrit))
				case d.Temp >= lim.DiskWarn:
					add("disk-temp-"+d.Name, levelWarn, "disque %s à %s (alerte dès %s)", d.Name, fmtT(d.Temp), fmtT(lim.DiskWarn))
				}
			}
			if d.Health != "" && d.Health != "PASSED" {
				add("disk-health-"+d.Name, levelCrit, "disque %s: test SMART %s", d.Name, d.Health)
			}
			if d.Reported > 0 {
				add("disk-errors-"+d.Name, levelWarn, "disque %s: %d erreur(s) de lecture non corrigée(s) cumulée(s)", d.Name, d.Reported)
			}
			// Aggravation depuis le premier relevé de cette session: c'est le signal le plus utile,
			// car un défaut ancien et stable ne dit pas si le disque continue de se dégrader.
			if b, ok := in.Baseline[d.Name]; ok {
				grow := (d.Reported - b.Reported) + (d.Reallocated - b.Reallocated) + (d.Pending - b.Pending)
				if grow > 0 {
					add("disk-growth-"+d.Name, levelCrit, "disque %s se dégrade: +%d erreur(s)/secteur(s) défectueux depuis %s",
						d.Name, grow, b.At.Format("02/01 15:04"))
				}
			}
			if d.Pending > 0 || d.Uncorrect > 0 {
				add("disk-bad-"+d.Name, levelCrit, "disque %s: %d secteur(s) en attente, %d non corrigeable(s)", d.Name, d.Pending, d.Uncorrect)
			} else if d.Reallocated > 0 {
				add("disk-realloc-"+d.Name, levelWarn, "disque %s: %d secteur(s) réalloué(s)", d.Name, d.Reallocated)
			}
		}
		if s.CPUOK {
			switch {
			case s.CPU >= lim.CPUCrit:
				add("cpu-temp", levelCrit, "processeur à %s (critique dès %s)", fmtT(s.CPU), fmtT(lim.CPUCrit))
			case s.CPU >= lim.CPUWarn:
				add("cpu-temp", levelWarn, "processeur à %s (alerte dès %s)", fmtT(s.CPU), fmtT(lim.CPUWarn))
			}
		}
		if name, t, ok := s.maxBoardTemp(); ok {
			switch {
			case t >= lim.BoardShutdown:
				add("board-temp", levelCrit, "carte mère (%s) à %s (critique dès %s)", name, fmtT(t), fmtT(lim.BoardShutdown))
			case t >= lim.BoardWarn:
				add("board-temp", levelWarn, "carte mère (%s) à %s (alerte dès %s)", name, fmtT(t), fmtT(lim.BoardWarn))
			}
		}
		if s.NASFans != nil {
			down := 0
			for _, n := range lim.NASFans {
				if rpm, ok := s.NASFans[n]; ok && rpm < lim.MinRPM {
					down++
					add(fmt.Sprintf("nasfan-%d", n), levelWarn, "ventilateur interne du NAS n°%d arrêté (%d tr/min)", n, rpm)
				}
			}
			if len(lim.NASFans) > 0 && down == len(lim.NASFans) {
				add("nasfan-all", levelCrit, "tous les ventilateurs internes du NAS sont arrêtés")
			}
		}
		if s.UPS.Present {
			switch {
			case strings.Contains(s.UPS.Status, "LB"):
				add("ups", levelCrit, "onduleur: batterie faible (%s, %.0f %%, autonomie %.0f min)", s.UPS.Status, s.UPS.Charge, s.UPS.RuntimeS/60)
			case strings.Contains(s.UPS.Status, "OB"):
				add("ups", levelWarn, "coupure secteur: le NAS est sur batterie (%.0f %%, autonomie %.0f min)", s.UPS.Charge, s.UPS.RuntimeS/60)
			}
		}
		for _, r := range s.RAID {
			avance := ""
			if r.Rebuilding && r.Progress > 0 {
				avance = fmt.Sprintf(", reconstruction à %.0f %%", r.Progress)
			} else if r.Rebuilding {
				avance = ", reconstruction en cours"
			}
			switch r.Status {
			case "degraded":
				// Une marge de sécurité encore confortable (ex.: RAID 1 à 8 copies) n'est qu'une alerte.
				level := levelWarn
				if r.remaining() <= 1 {
					level = levelCrit
				}
				add("raid-"+r.Name, level, "RAID %s dégradé (%d disques sur %d)%s", r.Name, r.Up, r.Total, avance)
				// Plus aucune redondance: alerte distincte, rappelée à fréquence normale.
				if r.remaining() <= 0 {
					add("raid-"+r.Name+"-severe", levelCrit, "RAID %s SANS REDONDANCE (%d disques sur %d): un disque de plus et les données sont perdues", r.Name, r.Up, r.Total)
				}
			case "resync":
				add("raid-"+r.Name, levelWarn, "RAID %s en resynchronisation%s", r.Name, avance)
			}
		}
		if s.VolOK {
			switch {
			case s.VolUsed >= lim.VolCrit:
				add("volume", levelCrit, "volume 1 rempli à %.0f %%", s.VolUsed)
			case s.VolUsed >= lim.VolWarn:
				add("volume", levelWarn, "volume 1 rempli à %.0f %%", s.VolUsed)
			}
		}
	}

	// Baie QNAP et prise Hue: indépendantes d'OpenLinkHub, donc évaluées avant le retour anticipé ci-dessous.
	for _, f := range qnapFindings(in.Qnap, in.Enforce, in.Maintenance, lim) {
		add(f.Key, f.Level, "%s", f.Text)
	}
	for _, f := range hueFindings(in.Hue, in.Enforce, in.Maintenance, lim) {
		add(f.Key, f.Level, "%s", f.Text)
	}

	// Meuble: ventilation Corsair et sondes du Commander DUO.
	if !in.DevsOK {
		if in.UnreachAt > 0 && in.APIDownFor >= in.UnreachAt {
			add("api", levelWarn, "OpenLinkHub injoignable depuis %s (le Hub garde ses réglages)", in.APIDownFor.Round(time.Second))
		}
		return finalize(out)
	}
	// Hub gelé ou sans courant: OpenLinkHub garde et affiche les dernières valeurs (nuit du 12/11/2026), donc des
	// ventilateurs « normaux » à vitesse rigoureusement constante ne prouvent rien.
	if lim.HubStale > 0 && in.Enforce && !in.Maintenance && in.FanStill >= lim.HubStale {
		add("hub-stale", levelWarn, "OpenLinkHub affiche exactement les mêmes vitesses de ventilateurs depuis %s: Hub gelé ou sans courant, les valeurs affichées ne sont plus fiables",
			in.FanStill.Round(time.Minute))
	}
	listed, running := 0, 0
	for _, d := range in.Devs {
		if !isFan(d) {
			continue
		}
		listed++
		if d.RPM >= lim.MinRPM {
			running++
		} else if in.Enforce && !in.Maintenance {
			add(fmt.Sprintf("corsair-fan-%d", d.ChannelID), levelWarn, "ventilateur du meuble canal %d arrêté (%d tr/min)", d.ChannelID, d.RPM)
		}
	}
	if in.Enforce && !in.Maintenance {
		lost := ""
		if in.LostLimit > 0 {
			lost = fmt.Sprintf(": arrêt du NAS demandé si elle dure plus de %s", fmtLimit(in.LostLimit))
		}
		st, down, okVent := ventilationState(in.Devs, lim)
		switch {
		case listed > 0 && running == 0:
			add("corsair-all", levelCrit, "tous les ventilateurs du meuble sont arrêtés%s", lost)
		case okVent && st == ventLost:
			add("corsair-extraction", levelCrit, "tous les ventilateurs d'extraction (canaux %s) sont arrêtés ou absents, ventilation perdue%s",
				joinChannels(down), lost)
		case okVent && st == ventDegraded:
			add("corsair-extraction", levelCrit, "%d ventilateur(s) d'extraction sur %d arrêté(s) ou absent(s) (canaux %s): ventilation dégradée, arrêt seulement si un disque atteint %s",
				len(down), len(lim.ExtractionFans), joinChannels(down), fmtT(lim.DiskShutdown))
		}
		if lim.CorsairFans > 0 && listed < lim.CorsairFans {
			add("corsair-missing", levelWarn, "%d ventilateur(s) du meuble listé(s) sur %d attendus", listed, lim.CorsairFans)
		}
	}
	for _, p := range []struct {
		label string
		ch    int
	}{{"NAS", in.NasCh}, {"QNAP", in.QnapCh}} {
		var t float64
		found := false
		for _, d := range in.Devs {
			if d.ChannelID == p.ch && d.IsTemperatureProbe && d.Temperature > 0 && d.Temperature <= 120 {
				t, found = d.Temperature, true
			}
		}
		key := fmt.Sprintf("probe-%s", p.label)
		switch {
		case !found:
			if in.Enforce {
				add(key+"-missing", levelWarn, "sonde du meuble %s absente ou illisible", p.label)
			}
		case t >= lim.ProbeCrit:
			add(key, levelCrit, "sonde du meuble %s à %s (critique dès %s)", p.label, fmtT(t), fmtT(lim.ProbeCrit))
		case t >= lim.ProbeWarn:
			add(key, levelWarn, "sonde du meuble %s à %s (alerte dès %s)", p.label, fmtT(t), fmtT(lim.ProbeWarn))
		}
		// Sonde figée: une vraie sonde bouge d'au moins un dixième de degré en quelques minutes. Valeur
		// identique depuis des heures = lecture bloquée (les ventilateurs, qui suivent la sonde, ne réagissent plus).
		if found && in.DevsOK && lim.ProbeFrozen > 0 && in.ProbeStill[p.ch] >= lim.ProbeFrozen {
			add(key+"-frozen", levelWarn, "sonde du meuble %s figée à %.1f °C depuis %s (valeur strictement identique): lecture probablement bloquée",
				p.label, t, in.ProbeStill[p.ch].Round(time.Minute))
		}
	}
	return finalize(out)
}

func finalize(f []finding) []finding {
	sort.Slice(f, func(i, j int) bool { return f[i].Key < f[j].Key })
	return f
}

// heat suit une température qui doit rester sous un seuil d'arrêt: prolongée pendant `hold`,
// ou immédiate sur deux relevés consécutifs. Un relevé n'est compté qu'une fois (voir at).
type heat struct {
	label     string
	sustained float64
	instant   float64
	since     time.Time
	n         int
	lastAt    time.Time
}

// update renvoie une raison d'arrêt non vide si les conditions sont réunies. `at` est l'horodatage
// du relevé: un même relevé relu plusieurs fois par la boucle principale ne compte qu'une fois.
func (h *heat) update(t float64, at, now time.Time, hold time.Duration) string {
	if at.Equal(h.lastAt) {
		return h.verdict(t, now, hold)
	}
	h.lastAt = at
	if t >= h.instant {
		h.n++
	} else {
		h.n = 0
	}
	if t >= h.sustained {
		if h.since.IsZero() {
			h.since = now
		}
	} else {
		h.since = time.Time{}
	}
	return h.verdict(t, now, hold)
}

func (h *heat) verdict(t float64, now time.Time, hold time.Duration) string {
	switch {
	case h.n >= 2:
		return fmt.Sprintf("%s à %s, seuil immédiat %s", h.label, fmtT(t), fmtT(h.instant))
	case !h.since.IsZero() && now.Sub(h.since) >= hold:
		return fmt.Sprintf("%s au-dessus de %s depuis %s (%s)", h.label, fmtT(h.sustained), now.Sub(h.since).Round(time.Second), fmtT(t))
	}
	return ""
}

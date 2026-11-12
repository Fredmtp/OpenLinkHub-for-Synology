package main

import (
	"fmt"
	"sync"
	"time"
)

// Suivi du nettoyage des données du RAID (« check » du noyau, lancé par DSM). Le 01/11/2026 la surveillance
// ne voyait pas la fin d'un nettoyage qui durait un jour: elle prévient maintenant au début (information
// discrète) et à la fin, avec la durée et le nombre d'incohérences trouvées par le noyau.

type scrubWatch struct {
	mu       sync.Mutex
	active   bool
	since    time.Time
	known    bool // le début a été vu (sinon la durée est inconnue: la surveillance a démarré en cours de route)
	lastPct  float64
	lastName string
}

// scrubNote est ce que scrubWatch.observe demande de signaler.
type scrubNote struct {
	Kind string // "start", "end" ou "" (rien)
	Msg  message
	Text string // ligne pour le journal des événements
}

func fmtDurScrub(d time.Duration) string {
	if d < time.Minute {
		return "moins d'une minute"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h == 0 {
		return fmt.Sprintf("%d min", m)
	}
	return fmt.Sprintf("%d h %02d", h, m)
}

// observe reçoit la photo du NAS (une par minute) et renvoie ce qu'il faut signaler.
func (w *scrubWatch) observe(s nasSnap, now time.Time) scrubNote {
	var data *raidInfo
	for i := range s.RAID {
		if data == nil || s.RAID[i].Total > data.Total {
			data = &s.RAID[i]
		}
	}
	if data == nil {
		return scrubNote{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case data.Scrubbing && !w.active:
		w.active, w.since, w.lastPct, w.lastName = true, now, data.Progress, data.Name
		// Premier relevé déjà avancé: la surveillance a démarré en cours de route (ou après le redémarrage du NAS).
		w.known = data.Progress < 1
		text := fmt.Sprintf("nettoyage des données du RAID %s en cours (%.0f %%)", data.Name, data.Progress)
		if w.known {
			text = fmt.Sprintf("nettoyage des données du RAID %s démarré", data.Name)
		}
		return scrubNote{Kind: "start", Text: text, Msg: message{
			Subject:  "[NAS meuble] nettoyage des données démarré",
			Body:     text + ".\nLes disques sont relus en continu pendant des heures (jusqu'à plus d'un jour sur 12 To): le NAS est plus sollicité et les disques un peu plus chauds. Ne pas éteindre le NAS pendant ce temps.\n",
			Short:    "Nettoyage des données du RAID en cours. Ne pas éteindre le NAS.",
			Priority: 2, Tags: []string{"broom"},
		}}
	case data.Scrubbing && w.active:
		w.lastPct = data.Progress
		return scrubNote{}
	case !data.Scrubbing && w.active:
		w.active = false
		d := now.Sub(w.since)
		if w.lastPct < 95 {
			// Disparu bien avant la fin: suspendu dans DSM ou annulé, pas terminé.
			text := fmt.Sprintf("nettoyage des données du RAID %s interrompu vers %.0f %% (suspendu ou annulé dans DSM)", data.Name, w.lastPct)
			return scrubNote{Kind: "end", Text: text, Msg: message{
				Subject:  "[NAS meuble] nettoyage des données interrompu",
				Body:     text + ".\n",
				Short:    text + ".",
				Priority: 2, Tags: []string{"pause_button"},
			}}
		}
		res := "résultat: aucune incohérence"
		prio := 3
		tags := []string{"white_check_mark"}
		switch {
		case data.Status != "ok":
			res = fmt.Sprintf("le RAID est maintenant « %s » (à vérifier tout de suite)", data.Status)
			prio, tags = 4, []string{"warning"}
		case data.MismatchOK && data.Mismatch > 0:
			res = fmt.Sprintf("%d incohérence(s) relevée(s) par le noyau (compteur mismatch_cnt): à examiner dans DSM", data.Mismatch)
			prio, tags = 4, []string{"warning"}
		case !data.MismatchOK:
			res = "résultat du noyau illisible"
		}
		text := fmt.Sprintf("nettoyage des données du RAID %s terminé: %s", data.Name, res)
		if w.known {
			text += fmt.Sprintf(" (durée mesurée %s)", fmtDurScrub(d))
		} else {
			text += fmt.Sprintf(" (suivi depuis %s seulement)", fmtDurScrub(d))
		}
		return scrubNote{Kind: "end", Text: text, Msg: message{
			Subject:  "[NAS meuble] nettoyage des données terminé",
			Body:     text + ".\n",
			Short:    text + ".",
			Priority: prio, Tags: tags,
		}}
	}
	return scrubNote{}
}

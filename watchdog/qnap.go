package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// Surveillance de la baie QNAP TR-004 branchée en USB (archives et sauvegarde). Elle est en RAID matériel:
// le NAS n'en voit qu'un seul disque virtuel, donc pas de SMART ni d'état du RAID (voir
// docs/supervision-surveillance.md). Ce qui reste visible depuis DSM, et que cette surveillance couvre:
//   - la disparition du périphérique (câble USB, alimentation, baie plantée);
//   - les erreurs d'entrée-sortie ou les réinitialisations signalées par le noyau sur ce périphérique.
// Activée par -qnap-vendor (texte du fabricant dans /proc/scsi/scsi, « QNAP »); vide = désactivée.

// qnapSnap est ce que collectNAS relève de la baie.
type qnapSnap struct {
	Checked bool // la surveillance est active et /proc/scsi/scsi a pu être lu
	Present bool
	Host    string // numéro d'hôte SCSI du périphérique (« 12 » pour scsi12), pour retrouver ses lignes du noyau
	Errors  int    // nombre de lignes d'erreur du noyau qui le concernent (cumul du journal du noyau)
}

func readFileString(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

var reScsiHost = regexp.MustCompile(`Host:\s*scsi(\d+)`)

// parseQnapScsi cherche un périphérique du fabricant donné dans /proc/scsi/scsi.
func parseQnapScsi(txt, vendor string) (present bool, host string) {
	if vendor == "" {
		return false, ""
	}
	cur := ""
	for _, line := range strings.Split(txt, "\n") {
		if m := reScsiHost.FindStringSubmatch(line); m != nil {
			cur = m[1]
			continue
		}
		if cur != "" && strings.Contains(line, "Vendor:") {
			f := strings.TrimSpace(line[strings.Index(line, "Vendor:")+len("Vendor:"):])
			if strings.HasPrefix(strings.ToLower(f), strings.ToLower(vendor)) {
				return true, cur
			}
		}
	}
	return false, ""
}

var reQnapErr = regexp.MustCompile(`(?i)I/O error|Buffer I/O|FAILED Result|Medium Error|Sense Key|timing out|timed out|task abort|uas_eh|reset (high|full|super)[- ]?speed|Unrecovered|Hardware Error|disconnect`)

// countQnapErrors compte, dans le journal du noyau, les lignes d'erreur qui mentionnent l'hôte SCSI de la baie.
func countQnapErrors(dmesg, host string) int {
	if host == "" {
		return 0
	}
	re := regexp.MustCompile(`\b(sd |scsi)` + regexp.QuoteMeta(host) + `\b|\bscsi_?host` + regexp.QuoteMeta(host) + `\b|sd ` + regexp.QuoteMeta(host) + `:`)
	n := 0
	for _, line := range strings.Split(dmesg, "\n") {
		if re.MatchString(line) && reQnapErr.MatchString(line) {
			n++
		}
	}
	return n
}

// readQnap relève la baie. L'hôte SCSI est connu seulement quand elle est présente; absente, Errors reste à 0.
func readQnap(vendor string) qnapSnap {
	if vendor == "" {
		return qnapSnap{}
	}
	b, err := readFileString("/proc/scsi/scsi")
	if err != nil {
		return qnapSnap{}
	}
	s := qnapSnap{Checked: true}
	s.Present, s.Host = parseQnapScsi(b, vendor)
	if s.Present {
		if out, _ := run(8*time.Second, "dmesg"); out != "" {
			s.Errors = countQnapErrors(out, s.Host)
		}
	}
	return s
}

// qnapTrack suit la baie d'une minute à l'autre: depuis quand elle est absente et quand le nombre d'erreurs a augmenté.
type qnapTrack struct {
	seen        bool
	prevErrors  int
	haveErrors  bool
	absentSince time.Time
	lastErrAt   time.Time
	lastNew     int
}

// update met à jour le suivi avec une nouvelle relève. L'appelant tient le verrou.
func (q *qnapTrack) update(s qnapSnap, now time.Time) {
	if !s.Checked {
		return
	}
	q.seen = true
	if s.Present {
		q.absentSince = time.Time{}
		if q.haveErrors && s.Errors > q.prevErrors {
			q.lastErrAt, q.lastNew = now, s.Errors-q.prevErrors
		}
		// Le journal du noyau peut tourner (cumul en baisse): on repart de la valeur actuelle.
		q.prevErrors, q.haveErrors = s.Errors, true
		return
	}
	if q.absentSince.IsZero() {
		q.absentSince = now
	}
	q.haveErrors = false // à son retour, on repart de zéro (les anciennes erreurs ne comptent pas)
}

// qnapState est la vue de ce suivi pour l'évaluation.
type qnapState struct {
	Tracked   bool
	AbsentFor time.Duration // 0 = présente
	ErrAgo    time.Duration // depuis les dernières nouvelles erreurs (0 = aucune)
	NewErrors int
}

func (q *qnapTrack) view(now time.Time) qnapState {
	if !q.seen {
		return qnapState{}
	}
	v := qnapState{Tracked: true}
	if !q.absentSince.IsZero() {
		v.AbsentFor = now.Sub(q.absentSince)
	}
	if !q.lastErrAt.IsZero() {
		v.ErrAgo, v.NewErrors = now.Sub(q.lastErrAt), q.lastNew
	}
	return v
}

// qnapFindings: absence (après un délai) et erreurs récentes du noyau.
func qnapFindings(q qnapState, enforce, maintenance bool, lim limits) []finding {
	if !q.Tracked || !enforce || maintenance {
		return nil
	}
	var out []finding
	if q.AbsentFor >= lim.QnapAbsent {
		out = append(out, finding{Key: "qnap-missing", Level: levelWarn,
			Text: fmt.Sprintf("baie QNAP absente depuis %s: câble USB, alimentation ou baie plantée (archives et sauvegarde inaccessibles)", q.AbsentFor.Round(time.Minute))})
	}
	if q.ErrAgo > 0 && q.ErrAgo < lim.QnapErrorsFor {
		out = append(out, finding{Key: "qnap-io", Level: levelWarn,
			Text: fmt.Sprintf("le noyau signale %d nouvelle(s) erreur(s) d'entrée-sortie ou réinitialisation USB sur la baie QNAP (il y a %s)", q.NewErrors, q.ErrAgo.Round(time.Minute))})
	}
	return out
}

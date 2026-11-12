package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Journal des événements (un par ligne, JSON), conservé 7 jours: il sert à l'IA pour rapprocher des
// événements espacés (une cause commune: vibration, alimentation…), au résumé (« événements des
// dernières 24 h ») et à l'utilisateur, qui peut confronter les dates à ses propres activités
// (impressions 3D, lave-linge…). Il survit aux relancements de la surveillance.

type eventRec struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"` // event, fresh, back
	Key     string    `json:"key"`
	Title   string    `json:"titre"`
	Text    string    `json:"texte"`
	Gravity string    `json:"gravite"`
}

type eventLog struct {
	mu   sync.Mutex
	path string
	keep time.Duration
	recs []eventRec
}

// newEventLog charge le fichier (s'il existe) et en retire ce qui dépasse la durée de conservation.
func newEventLog(path string, now time.Time) *eventLog {
	l := &eventLog{path: path, keep: 7 * 24 * time.Hour}
	if path == "" {
		return l
	}
	f, err := os.Open(path)
	if err != nil {
		return l
	}
	defer f.Close()
	dropped := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r eventRec
		if json.Unmarshal([]byte(line), &r) != nil || r.At.IsZero() {
			dropped = true
			continue
		}
		if now.Sub(r.At) > l.keep {
			dropped = true
			continue
		}
		l.recs = append(l.recs, r)
	}
	sort.SliceStable(l.recs, func(i, j int) bool { return l.recs[i].At.Before(l.recs[j].At) })
	if dropped {
		l.rewrite()
	}
	return l
}

func (l *eventLog) rewrite() {
	if l.path == "" {
		return
	}
	var b strings.Builder
	for _, r := range l.recs {
		line, _ := json.Marshal(r)
		b.Write(line)
		b.WriteByte('\n')
	}
	tmp := l.path + ".tmp"
	if os.WriteFile(tmp, []byte(b.String()), 0o644) == nil {
		_ = os.Rename(tmp, l.path)
	}
}

// add enregistre un événement en mémoire et dans le fichier (ajout en fin de fichier).
func (l *eventLog) add(r eventRec) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recs = append(l.recs, r)
	if l.path == "" {
		return
	}
	line, _ := json.Marshal(r)
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		logf("ATTENTION: journal des événements non écrit (%v)", err)
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}

// since renvoie une copie des événements postérieurs à t, du plus ancien au plus récent.
func (l *eventLog) since(t time.Time) []eventRec {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []eventRec
	for _, r := range l.recs {
		if !r.At.Before(t) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// lines met les événements en phrases courtes pour l'IA (jamais plus de max lignes, les plus récentes).
func eventLines(recs []eventRec, max int) []string {
	if len(recs) > max {
		recs = recs[len(recs)-max:]
	}
	var out []string
	for _, r := range recs {
		out = append(out, fmt.Sprintf("%s — %s (%s)", r.At.Format("02/01 15:04"), r.Text, r.Gravity))
	}
	return out
}

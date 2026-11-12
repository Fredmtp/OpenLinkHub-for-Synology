package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Graphiques des températures. L'API /api/releves relit les fichiers CSV de l'enregistreur (record.go),
// regroupe les lignes par intervalles de temps (moyenne et maximum de chaque colonne par intervalle) et
// renvoie un JSON léger que la page /static/nas-graphiques.html d'OpenLinkHub trace (autre port: d'où
// l'en-tête CORS). Lecture seule, aucune donnée sensible. Les intervalles sans mesure (NAS éteint la nuit)
// restent vides (null): la courbe est interrompue au lieu d'inventer des valeurs.

const (
	maxGraphDays       = 400  // durée maximale demandée
	defaultGraphPoints = 900  // nombre d'intervalles par défaut
	maxGraphPoints     = 3000 // plafond du nombre d'intervalles
	graphTimeLayout    = "2006-01-02 15:04:05"
)

type graphSeries struct {
	Key string     `json:"key"`
	Avg []*float64 `json:"avg"`
	Max []*float64 `json:"max"`
}

type graphData struct {
	From   int64         `json:"from"`
	To     int64         `json:"to"`
	Step   int64         `json:"step"` // secondes par intervalle
	T      []int64       `json:"t"`    // instant central de chaque intervalle (secondes Unix)
	Series []graphSeries `json:"series"`
	Lignes int           `json:"lignes"` // nombre de lignes de relevé lues dans la période
}

type graphAcc struct {
	sum []float64
	n   []int32
	max []float64
}

func newGraphAcc(nb int) *graphAcc {
	a := &graphAcc{sum: make([]float64, nb), n: make([]int32, nb), max: make([]float64, nb)}
	for i := range a.max {
		a.max[i] = math.Inf(-1)
	}
	return a
}

// ventCodes: l'état de la ventilation du CSV devient un niveau (0 = ok ... 3 = perdue, -1 = inconnue).
var ventCodes = map[string]float64{"ok": 0, "partielle": 1, "degradee": 2, "perdue": 3, "inconnue": -1}

// cellValue convertit une cellule du CSV; ok = false si elle est vide ou illisible.
func cellValue(col, cell string) (float64, bool) {
	cell = strings.TrimSpace(cell)
	if cell == "" {
		return 0, false
	}
	if col == "ventilation" {
		v, ok := ventCodes[cell]
		return v, ok
	}
	v, err := strconv.ParseFloat(cell, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func round1(v float64) *float64 {
	r := math.Round(v*10) / 10
	return &r
}

// readReleves relit les CSV de dir pour la période [from, to[ et regroupe les lignes par intervalles de
// step secondes. Les lignes d'en-tête réécrites en cours de fichier (colonnes changées) sont suivies.
func readReleves(dir string, from, to time.Time, step int64) (graphData, error) {
	fromU, toU := from.Unix(), to.Unix()
	if toU <= fromU || step <= 0 {
		return graphData{}, errors.New("période invalide")
	}
	nb := int((toU - fromU + step - 1) / step)
	data := graphData{From: fromU, To: toU, Step: step, T: make([]int64, nb), Series: []graphSeries{}}
	for i := range data.T {
		data.T[i] = fromU + int64(i)*step + step/2
	}
	accs := map[string]*graphAcc{}
	var order []string

	for m := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.Local); !m.After(to); m = m.AddDate(0, 1, 0) {
		path := filepath.Join(dir, "temperatures-"+m.Format("2006-01")+".csv")
		f, err := os.Open(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return data, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		var header []string
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "horodatage,") {
				header = strings.Split(line, ",")
				continue
			}
			if header == nil {
				continue
			}
			parts := strings.Split(line, ",")
			if len(parts) != len(header) {
				continue
			}
			ts, err := time.ParseInLocation(graphTimeLayout, parts[0], time.Local)
			if err != nil {
				continue
			}
			u := ts.Unix()
			if u < fromU || u >= toU {
				continue
			}
			idx := int((u - fromU) / step)
			data.Lignes++
			for i := 1; i < len(parts); i++ {
				v, ok := cellValue(header[i], parts[i])
				if !ok {
					continue
				}
				a := accs[header[i]]
				if a == nil {
					a = newGraphAcc(nb)
					accs[header[i]] = a
					order = append(order, header[i])
				}
				a.sum[idx] += v
				a.n[idx]++
				if v > a.max[idx] {
					a.max[idx] = v
				}
			}
		}
		err = sc.Err()
		f.Close()
		if err != nil {
			return data, err
		}
	}

	for _, key := range order {
		a := accs[key]
		s := graphSeries{Key: key, Avg: make([]*float64, nb), Max: make([]*float64, nb)}
		for i := 0; i < nb; i++ {
			if a.n[i] == 0 {
				continue
			}
			if key == "ventilation" { // un niveau d'alerte: le pire de l'intervalle, pas une moyenne
				s.Avg[i] = round1(a.max[i])
			} else {
				s.Avg[i] = round1(a.sum[i] / float64(a.n[i]))
			}
			s.Max[i] = round1(a.max[i])
		}
		data.Series = append(data.Series, s)
	}
	return data, nil
}

// firstReleve donne l'instant du plus ancien relevé enregistré (0 si aucun).
func firstReleve(dir string) int64 {
	files, _ := filepath.Glob(filepath.Join(dir, "temperatures-*.csv"))
	sort.Strings(files)
	for _, p := range files {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "horodatage,") || len(line) < 19 {
				continue
			}
			if ts, err := time.ParseInLocation(graphTimeLayout, line[:19], time.Local); err == nil {
				f.Close()
				return ts.Unix()
			}
		}
		f.Close()
	}
	return 0
}

// graphStep choisit la durée d'un intervalle (multiple de 60 s) pour viser au plus `points` intervalles.
func graphStep(from, to time.Time, points int) int64 {
	if points <= 0 {
		points = defaultGraphPoints
	}
	if points > maxGraphPoints {
		points = maxGraphPoints
	}
	span := to.Unix() - from.Unix()
	step := (span + int64(points) - 1) / int64(points)
	if step < 60 {
		return 60
	}
	return (step + 59) / 60 * 60
}

type graphMeta struct {
	Label string `json:"label"`
	Unit  string `json:"unit"`
	Group string `json:"group"`
}

// graphMetaFor décrit chaque colonne connue (libellé lisible, unité, famille). Une colonne inconnue
// (ajoutée plus tard) est affichée sous son nom brut par la page.
func graphMetaFor(l limits, nasCh, qnapCh int) map[string]graphMeta {
	m := map[string]graphMeta{
		"disque_max":     {"Disque le plus chaud", "°C", "disques"},
		"processeur":     {"Processeur", "°C", "systeme"},
		"carte_mere_max": {"Carte mère (max)", "°C", "systeme"},
		"sonde_nas":      {fmt.Sprintf("Air du meuble, sonde NAS (canal %d)", nasCh), "°C", "meuble"},
		"sonde_qnap":     {fmt.Sprintf("Air du meuble, sonde QNAP (canal %d)", qnapCh), "°C", "meuble"},
		"ventilation":    {"Ventilation (0 ok, 1 partielle, 2 dégradée, 3 perdue)", "", "etats"},
		"maintenance":    {"Maintenance", "", "etats"},
		"nettoyage_pct":  {"Nettoyage des données RAID", "%", "etats"},
	}
	for i := 1; i <= 8; i++ {
		m[fmt.Sprintf("disque_sata%d", i)] = graphMeta{fmt.Sprintf("Disque %d", i), "°C", "disques"}
	}
	for _, n := range l.NASFans {
		m[fmt.Sprintf("ventilo_nas_%d_tr_min", n)] = graphMeta{fmt.Sprintf("Ventilateur du NAS n°%d", n), "tr/min", "ventilos"}
	}
	for _, ch := range l.ExtractionFans {
		m[fmt.Sprintf("ventilo_ch%d_tr_min", ch)] = graphMeta{fmt.Sprintf("Extraction du meuble (canal %d)", ch), "tr/min", "ventilos"}
	}
	if l.IntakeFan > 0 {
		m[fmt.Sprintf("ventilo_ch%d_tr_min", l.IntakeFan)] = graphMeta{fmt.Sprintf("Admission du meuble (canal %d)", l.IntakeFan), "tr/min", "ventilos"}
	}
	return m
}

type graphEvent struct {
	At      int64  `json:"t"`
	Title   string `json:"titre"`
	Text    string `json:"texte"`
	Gravity string `json:"gravite"`
}

var graphMu sync.Mutex // une seule relecture des CSV à la fois (page ouverte plusieurs fois)

// releves répond à GET /api/releves?from=<s Unix>&to=<s Unix>&points=<n>.
func (w *webCtx) releves(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Access-Control-Allow-Origin", "*")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	fail := func(code int, msg string) {
		rw.WriteHeader(code)
		json.NewEncoder(rw).Encode(map[string]string{"erreur": msg})
	}
	if r.Method != http.MethodGet {
		fail(http.StatusMethodNotAllowed, "méthode non autorisée")
		return
	}
	if w.recDir == "" {
		fail(http.StatusNotFound, "l'enregistrement des températures est désactivé (option -record)")
		return
	}
	q := r.URL.Query()
	now := time.Now()
	to, from := now, now.Add(-24*time.Hour)
	if s := q.Get("to"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			fail(http.StatusBadRequest, "paramètre to invalide (secondes Unix attendues)")
			return
		}
		to = time.Unix(v, 0)
	}
	if s := q.Get("from"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			fail(http.StatusBadRequest, "paramètre from invalide (secondes Unix attendues)")
			return
		}
		from = time.Unix(v, 0)
	} else if q.Get("to") != "" {
		from = to.Add(-24 * time.Hour)
	}
	if !to.After(from) {
		fail(http.StatusBadRequest, "la fin doit être après le début")
		return
	}
	if to.Sub(from) > maxGraphDays*24*time.Hour {
		fail(http.StatusBadRequest, fmt.Sprintf("période trop longue (%d jours au plus)", maxGraphDays))
		return
	}
	points, _ := strconv.Atoi(q.Get("points"))

	graphMu.Lock()
	data, err := readReleves(w.recDir, from, to, graphStep(from, to, points))
	first := firstReleve(w.recDir)
	graphMu.Unlock()
	if err != nil {
		fail(http.StatusInternalServerError, "lecture des relevés impossible: "+err.Error())
		return
	}

	events := []graphEvent{}
	if w.ev != nil {
		for _, e := range w.ev.since(from) {
			if e.At.Before(to) && e.Gravity != "" {
				events = append(events, graphEvent{e.At.Unix(), e.Title, e.Text, e.Gravity})
			}
		}
		if len(events) > 300 {
			events = events[len(events)-300:]
		}
	}
	json.NewEncoder(rw).Encode(map[string]any{
		"from": data.From, "to": data.To, "step": data.Step, "t": data.T, "series": data.Series,
		"lignes": data.Lignes, "debut": first, "maintenant": now.Unix(),
		"meta": graphMetaFor(lim, w.nasCh, w.qnapCh),
		"seuils": map[string][2]float64{
			"disques":    {lim.DiskWarn, lim.DiskShutdown},
			"sondes":     {lim.ProbeWarn, lim.ProbeCrit},
			"processeur": {lim.CPUWarn, lim.CPUCrit},
			"carte":      {lim.BoardWarn, lim.BoardShutdown},
		},
		"evenements": events,
	})
}

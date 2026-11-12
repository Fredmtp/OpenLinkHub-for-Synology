package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func day(y int, mo time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, mo, d, h, mi, s, 0, time.Local)
}

func writeCSV(t *testing.T, dir, month, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "temperatures-"+month+".csv"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findSeries(d graphData, key string) *graphSeries {
	for i := range d.Series {
		if d.Series[i].Key == key {
			return &d.Series[i]
		}
	}
	return nil
}

func val(p *float64) float64 {
	if p == nil {
		return -999
	}
	return *p
}

func TestReadRelevesRegroupeParIntervalle(t *testing.T) {
	dir := t.TempDir()
	writeCSV(t, dir, "2026-11", strings.Join([]string{
		"horodatage,disque_sata1,disque_max,sonde_nas,ventilation",
		"2026-11-10 09:59:00,30,30,25.0,ok", // avant la période
		"2026-11-10 10:00:00,34,35,26.0,ok",
		"2026-11-10 10:01:00,36,37,27.0,partielle",
		"2026-11-10 10:02:00,,38,,ok", // cellules vides
		"2026-11-10 10:05:00,40,41,28.0,ok",
		"ligne abîmée sans virgules",
		"2026-11-10 10:06:00,42,43,29.0",        // colonnes manquantes
		"2026-11-10 10:10:00,50,51,30.0,perdue", // fin exclue
	}, "\n")+"\n")

	d, err := readReleves(dir, day(2026, 11, 10, 10, 0, 0), day(2026, 11, 10, 10, 10, 0), 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.T) != 2 || d.Step != 300 {
		t.Fatalf("2 intervalles de 300 s attendus: %d intervalles, pas %d", len(d.T), d.Step)
	}
	if d.Lignes != 4 {
		t.Errorf("4 lignes valides dans la période, lu %d", d.Lignes)
	}
	max := findSeries(d, "disque_max")
	if max == nil || val(max.Avg[0]) != 36.7 || val(max.Max[0]) != 38 || val(max.Avg[1]) != 41 {
		t.Errorf("disque_max inattendu: %+v", max)
	}
	s := findSeries(d, "sonde_nas")
	if s == nil || val(s.Avg[0]) != 26.5 || val(s.Max[0]) != 27 { // la cellule vide est ignorée, pas comptée comme 0
		t.Errorf("sonde_nas: la cellule vide ne doit pas compter: %+v", s)
	}
	v := findSeries(d, "ventilation")
	if v == nil || val(v.Avg[0]) != 1 || val(v.Avg[1]) != 0 {
		t.Errorf("ventilation: le pire niveau de l'intervalle attendu (1 puis 0): %+v", v)
	}
	if d.T[0] != day(2026, 11, 10, 10, 0, 0).Unix()+150 {
		t.Errorf("instant central du 1er intervalle incorrect: %d", d.T[0])
	}
}

func TestReadRelevesIntervalleVideEtEnTeteChange(t *testing.T) {
	dir := t.TempDir()
	// Colonnes modifiées au milieu du mois (disque remplacé): la 2e en-tête s'applique aux lignes suivantes.
	writeCSV(t, dir, "2026-11", strings.Join([]string{
		"horodatage,disque_max,nettoyage_pct",
		"2026-11-10 10:00:00,35,0",
		"horodatage,disque_max,sonde_qnap,nettoyage_pct",
		"2026-11-10 10:20:00,37,24.5,12.5",
	}, "\n")+"\n")
	d, err := readReleves(dir, day(2026, 11, 10, 10, 0, 0), day(2026, 11, 10, 10, 30, 0), 600)
	if err != nil {
		t.Fatal(err)
	}
	max := findSeries(d, "disque_max")
	if max == nil || val(max.Avg[0]) != 35 || max.Avg[1] != nil || val(max.Avg[2]) != 37 {
		t.Errorf("l'intervalle sans mesure doit rester vide (null): %+v", max)
	}
	q := findSeries(d, "sonde_qnap")
	if q == nil || q.Avg[0] != nil || val(q.Avg[2]) != 24.5 {
		t.Errorf("colonne apparue après le changement d'en-tête: %+v", q)
	}
	n := findSeries(d, "nettoyage_pct")
	if n == nil || val(n.Avg[2]) != 12.5 {
		t.Errorf("nettoyage_pct: %+v", n)
	}
}

func TestReadRelevesSurDeuxMois(t *testing.T) {
	dir := t.TempDir()
	writeCSV(t, dir, "2026-10", "horodatage,disque_max\n2026-10-31 23:55:00,40\n")
	writeCSV(t, dir, "2026-11", "horodatage,disque_max\n2026-11-01 00:03:00,42\n")
	d, err := readReleves(dir, day(2026, 10, 31, 23, 50, 0), day(2026, 11, 1, 0, 10, 0), 600)
	if err != nil {
		t.Fatal(err)
	}
	s := findSeries(d, "disque_max")
	if s == nil || d.Lignes != 2 || val(s.Avg[0]) != 40 || val(s.Avg[1]) != 42 {
		t.Errorf("les deux fichiers mensuels doivent être lus: %+v (%d lignes)", s, d.Lignes)
	}
}

func TestReadRelevesSansFichierNiErreur(t *testing.T) {
	d, err := readReleves(t.TempDir(), day(2026, 11, 10, 0, 0, 0), day(2026, 11, 11, 0, 0, 0), 600)
	if err != nil || len(d.Series) != 0 || d.Lignes != 0 || len(d.T) != 144 {
		t.Errorf("aucun fichier: pas d'erreur, pas de série (%v, %d séries, %d lignes)", err, len(d.Series), d.Lignes)
	}
	if _, err := readReleves(t.TempDir(), day(2026, 11, 11, 0, 0, 0), day(2026, 11, 10, 0, 0, 0), 60); err == nil {
		t.Error("une période inversée doit être refusée")
	}
}

func TestGraphStep(t *testing.T) {
	from := day(2026, 11, 10, 0, 0, 0)
	cases := []struct {
		to     time.Time
		points int
		want   int64
	}{
		{from.Add(10 * time.Minute), 900, 60},      // courte période: une ligne par minute
		{from.Add(24 * time.Hour), 900, 120},       // 86400 / 900 = 96 s, arrondi à la minute supérieure
		{from.Add(30 * 24 * time.Hour), 900, 2880}, // 30 jours
		{from.Add(24 * time.Hour), 0, 120},         // points absents: valeur par défaut
		{from.Add(24 * time.Hour), 100000, 60},     // plafonné à 3000 points
		{from.Add(400 * 24 * time.Hour), 3000, 11520},
	}
	for _, c := range cases {
		if got := graphStep(from, c.to, c.points); got != c.want {
			t.Errorf("durée %v, %d points: pas %d attendu, reçu %d", c.to.Sub(from), c.points, c.want, got)
		}
	}
}

func TestFirstReleve(t *testing.T) {
	dir := t.TempDir()
	if firstReleve(dir) != 0 {
		t.Error("aucun fichier: 0 attendu")
	}
	writeCSV(t, dir, "2026-11", "horodatage,disque_max\n2026-11-02 08:00:00,35\n")
	writeCSV(t, dir, "2026-10", "horodatage,disque_max\n2026-10-28 23:40:00,33\n")
	if got, want := firstReleve(dir), day(2026, 10, 28, 23, 40, 0).Unix(); got != want {
		t.Errorf("le plus ancien relevé attendu (%d), reçu %d", want, got)
	}
}

func apiGet(t *testing.T, w *webCtx, query string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	w.releves(rec, httptest.NewRequest(http.MethodGet, "/api/releves"+query, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("réponse non JSON (%d): %s", rec.Code, rec.Body.String())
	}
	return rec, body
}

func TestApiRelevesReponseEtGardeFous(t *testing.T) {
	dir := t.TempDir()
	writeCSV(t, dir, "2026-11", "horodatage,disque_max,sonde_nas\n2026-11-10 10:00:00,35,26.0\n2026-11-10 10:01:00,36,27.0\n")
	w := &webCtx{recDir: dir, nasCh: 13, qnapCh: 14, ev: newEventLog(filepath.Join(t.TempDir(), "ev.jsonl"), time.Now())}
	w.ev.add(eventRec{At: day(2026, 11, 10, 10, 1, 0), Kind: "event", Key: "x", Title: "Alerte d'essai", Text: "texte", Gravity: "haute"})

	from, to := day(2026, 11, 10, 9, 0, 0).Unix(), day(2026, 11, 10, 11, 0, 0).Unix()
	rec, body := apiGet(t, w, "?from="+unixStr(from)+"&to="+unixStr(to)+"&points=120")
	if rec.Code != 200 || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("200 et en-tête CORS attendus: %d %v", rec.Code, rec.Header())
	}
	if body["lignes"].(float64) != 2 || body["step"].(float64) != 60 {
		t.Errorf("2 lignes et pas de 60 s attendus: %v", body)
	}
	meta := body["meta"].(map[string]any)
	if m := meta["sonde_nas"].(map[string]any); !strings.Contains(m["label"].(string), "canal 13") || m["unit"] != "°C" {
		t.Errorf("libellé de la sonde NAS: %v", m)
	}
	if _, ok := meta["ventilo_ch15_tr_min"]; !ok {
		t.Error("le ventilateur d'admission (canal 15) doit être décrit")
	}
	seuils := body["seuils"].(map[string]any)
	if d := seuils["disques"].([]any); d[0].(float64) != 40 || d[1].(float64) != 45 {
		t.Errorf("seuils des disques: %v", d)
	}
	if ev := body["evenements"].([]any); len(ev) != 1 || ev[0].(map[string]any)["titre"] != "Alerte d'essai" {
		t.Errorf("l'événement de la période doit être joint: %v", body["evenements"])
	}
	if body["debut"].(float64) != float64(day(2026, 11, 10, 10, 0, 0).Unix()) {
		t.Errorf("début des relevés: %v", body["debut"])
	}

	for _, c := range []struct{ q, why string }{
		{"?from=abc", "from illisible"},
		{"?to=abc", "to illisible"},
		{"?from=" + unixStr(to) + "&to=" + unixStr(from), "période inversée"},
		{"?from=0&to=" + unixStr(to), "période de plus de 400 jours"},
	} {
		if rec, body := apiGet(t, w, c.q); rec.Code != 400 || body["erreur"] == nil {
			t.Errorf("%s: 400 avec message attendus, reçu %d %v", c.why, rec.Code, body)
		}
	}

	rec2 := httptest.NewRecorder()
	w.releves(rec2, httptest.NewRequest(http.MethodPost, "/api/releves", nil))
	if rec2.Code != http.StatusMethodNotAllowed {
		t.Errorf("seule la méthode GET est permise: %d", rec2.Code)
	}
	off := &webCtx{}
	if rec, body := apiGet(t, off, ""); rec.Code != 404 || body["erreur"] == nil {
		t.Errorf("sans -record: 404 avec message attendus, reçu %d %v", rec.Code, body)
	}

	// période par défaut (24 h) sans paramètre: pas d'erreur, séries vides
	if rec, body := apiGet(t, w, ""); rec.Code != 200 || body["series"] == nil || body["t"] == nil {
		t.Errorf("période par défaut: %d %v", rec.Code, body)
	}
}

func unixStr(v int64) string { return strconv.FormatInt(v, 10) }

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func recLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func cell(row []recCol, name string) string {
	for _, c := range row {
		if c.Name == name {
			return c.Val
		}
	}
	return "<absent>"
}

func TestRecordRowNominal(t *testing.T) {
	in := input()
	row := recordRow(in.NAS, true, in.Devs, true, defaultLimits(), 13, 14, false)
	want := map[string]string{
		"disque_sata1": "32", "disque_sata2": "38", "disque_max": "38", "processeur": "44.0", "carte_mere_max": "35.0",
		"ventilo_nas_1_tr_min": "1338", "sonde_nas": "27.0", "sonde_qnap": "23.0",
		"ventilo_ch1_tr_min": "770", "ventilo_ch3_tr_min": "770", "ventilo_ch15_tr_min": "770",
		"ventilation": "ok", "maintenance": "0",
	}
	for k, v := range want {
		if got := cell(row, k); got != v {
			t.Errorf("%s = %q, attendu %q", k, got, v)
		}
	}
}

func TestRecordRowAbsentFanIsEmptyAndStoppedFanIsZero(t *testing.T) {
	in := input()
	devs := []device{fan(1, 0), fan(3, 770), fan(15, 770), pr(13, 27), pr(14, 23)} // canal 2 absent, canal 1 à l'arrêt
	row := recordRow(in.NAS, true, devs, true, defaultLimits(), 13, 14, false)
	if got := cell(row, "ventilo_ch1_tr_min"); got != "0" {
		t.Errorf("ventilateur arrêté: %q, attendu 0", got)
	}
	if got := cell(row, "ventilo_ch2_tr_min"); got != "" {
		t.Errorf("ventilateur absent: %q, attendu une cellule vide", got)
	}
	if got := cell(row, "ventilation"); got != "degradee" {
		t.Errorf("deux extracteurs arrêtés ou absents: %q, attendu degradee", got)
	}
}

func TestRecordRowWithoutHubKeepsColumns(t *testing.T) {
	in := input()
	row := recordRow(in.NAS, true, nil, false, defaultLimits(), 13, 14, true)
	if cell(row, "sonde_nas") != "" || cell(row, "ventilo_ch1_tr_min") != "" {
		t.Errorf("Hub injoignable: cellules vides attendues: %+v", row)
	}
	if cell(row, "ventilation") != "inconnue" || cell(row, "maintenance") != "1" {
		t.Errorf("état attendu: inconnue / maintenance: %+v", row)
	}
	if cell(row, "disque_max") != "38" {
		t.Errorf("les disques restent mesurés sans le Hub: %+v", row)
	}
}

func TestRecordRowStandbyDiskHasNoTemperature(t *testing.T) {
	in := input()
	in.NAS.Disks[0].Standby = true
	row := recordRow(in.NAS, true, in.Devs, true, defaultLimits(), 13, 14, false)
	if got := cell(row, "disque_sata1"); got != "" {
		t.Errorf("disque en veille: %q, attendu vide", got)
	}
}

func TestRecorderWritesHeaderOnceAndAppends(t *testing.T) {
	dir := t.TempDir()
	r := newRecorder(dir)
	in := input()
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		row := recordRow(in.NAS, true, in.Devs, true, defaultLimits(), 13, 14, false)
		if err := r.write(t0.Add(time.Duration(i)*time.Minute), row); err != nil {
			t.Fatal(err)
		}
	}
	lines := recLines(t, filepath.Join(dir, "temperatures-2026-10.csv"))
	if len(lines) != 4 {
		t.Fatalf("%d lignes, attendu 1 en-tête + 3 relevés: %v", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], "horodatage,disque_sata1,") || !strings.HasPrefix(lines[1], "2026-10-28 12:00:00,32,") {
		t.Fatalf("format inattendu: %q / %q", lines[0], lines[1])
	}
	if strings.Count(lines[0], ",") != strings.Count(lines[1], ",") {
		t.Fatalf("en-tête et relevé n'ont pas le même nombre de colonnes")
	}
}

func TestRecorderRewritesHeaderWhenColumnsChange(t *testing.T) {
	dir := t.TempDir()
	r := newRecorder(dir)
	in := input()
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	r.write(t0, recordRow(in.NAS, true, in.Devs, true, defaultLimits(), 13, 14, false))
	in.NAS.Disks = append(in.NAS.Disks, diskInfo{Name: "sata3", Temp: 30, TempOK: true})
	r.write(t0.Add(time.Minute), recordRow(in.NAS, true, in.Devs, true, defaultLimits(), 13, 14, false))
	lines := recLines(t, filepath.Join(dir, "temperatures-2026-10.csv"))
	headers := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "horodatage,") {
			headers++
		}
	}
	if headers != 2 || len(lines) != 4 {
		t.Fatalf("2 en-têtes et 2 relevés attendus: %v", lines)
	}
}

func TestRecorderRollsOverEachMonth(t *testing.T) {
	dir := t.TempDir()
	r := newRecorder(dir)
	in := input()
	row := recordRow(in.NAS, true, in.Devs, true, defaultLimits(), 13, 14, false)
	r.write(time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC), row)
	r.write(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), row)
	for _, f := range []string{"temperatures-2026-10.csv", "temperatures-2026-11.csv"} {
		if got := recLines(t, filepath.Join(dir, f)); len(got) != 2 {
			t.Fatalf("%s: %v", f, got)
		}
	}
}

func TestRecorderReportsUnwritableFolder(t *testing.T) {
	f := filepath.Join(t.TempDir(), "fichier")
	os.WriteFile(f, []byte("x"), 0o644)
	r := newRecorder(filepath.Join(f, "sous-dossier")) // impossible: « fichier » n'est pas un dossier
	in := input()
	if err := r.write(time.Now(), recordRow(in.NAS, true, in.Devs, true, defaultLimits(), 13, 14, false)); err == nil {
		t.Fatal("une erreur d'écriture doit être renvoyée, pas avalée")
	}
}

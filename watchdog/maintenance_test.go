package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMaintenanceSuspendsFanShutdown(t *testing.T) {
	withMaintenance(t, time.Now().Add(time.Hour))
	m := newMon() // started = Unix 0: le délai de grâce est largement passé
	now := time.Now()
	bad := meuble(0, 0, 0, 0)
	for s := 0; s < 600; s += 10 {
		if r := m.evaluate(bad, now.Add(time.Duration(s)*time.Second)); r != "" {
			t.Fatalf("la maintenance doit suspendre l'arrêt ventilation: %s", r)
		}
	}
}

func TestMaintenanceEndRestartsCountdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maintenance")
	old := *maintFile
	*maintFile = path
	defer func() { *maintFile = old }()
	now := time.Now()
	if err := os.WriteFile(path, []byte(strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10)), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newMon()
	bad := meuble(0, 0, 0, 620) // perte totale de la ventilation
	m.evaluate(bad, now)
	os.Remove(path) // fin de la maintenance, ventilateurs toujours arrêtés
	restart := now.Add(20 * time.Second)
	if r := m.evaluate(bad, restart); r != "" {
		t.Fatalf("pas d'arrêt immédiat à la fin de la maintenance: %s", r)
	}
	if r := m.evaluate(bad, restart.Add(*fanLostFor-10*time.Second)); r != "" {
		t.Fatalf("arrêt avant le délai après la fin: %s", r)
	}
	if r := m.evaluate(bad, restart.Add(*fanLostFor+time.Second)); r == "" {
		t.Fatal("arrêt attendu une fois le délai écoulé après la fin de la maintenance")
	}
}

func TestMaintenanceKeepsThermalProtection(t *testing.T) {
	withMaintenance(t, time.Now().Add(time.Hour))
	m := newMon()
	now := time.Now()
	hot := append(meuble(0, 0, 0, 0)[:4], pr(13, 46), pr(14, 23))
	m.evaluate(hot, now)
	if r := m.evaluate(hot, now.Add(181*time.Second)); r == "" {
		t.Fatal("la chaleur prolongée doit toujours éteindre le NAS pendant la maintenance")
	}
}

func TestMaintenanceUntilParsing(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name, content string, mtime time.Time) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}
	sec := func(d time.Duration) string { return strconv.FormatInt(now.Add(d).Unix(), 10) }
	if _, on := maintenanceUntil("", now); on {
		t.Fatal("sans fichier configuré, pas de veille")
	}
	if _, on := maintenanceUntil(filepath.Join(dir, "absent"), now); on {
		t.Fatal("fichier absent: pas de veille")
	}
	if _, on := maintenanceUntil(write("vide", "", now), now); on {
		t.Fatal("fichier vide: pas de veille")
	}
	if _, on := maintenanceUntil(write("texte", "bientôt", now), now); on {
		t.Fatal("contenu illisible: pas de veille")
	}
	if _, on := maintenanceUntil(write("perime", sec(-time.Minute), now.Add(-time.Hour)), now); on {
		t.Fatal("veille périmée: inactive")
	}
	until, on := maintenanceUntil(write("ok", sec(30*time.Minute), now), now)
	if !on || until.Unix() != now.Add(30*time.Minute).Unix() {
		t.Fatalf("veille de 30 min attendue, reçu %v %v", until, on)
	}
	// Une date lointaine est plafonnée à 4 h après l'écriture du fichier.
	until, on = maintenanceUntil(write("loin", sec(48*time.Hour), now), now)
	if !on || until.After(now.Add(maintenanceMax+time.Second)) {
		t.Fatalf("plafond de 4 h attendu, reçu %v %v", until, on)
	}
	// Fichier écrit il y a plus de 4 h avec une date lointaine: plus de veille.
	if _, on = maintenanceUntil(write("oublie", sec(48*time.Hour), now.Add(-5*time.Hour)), now); on {
		t.Fatal("un fichier oublié depuis plus de 4 h ne doit plus suspendre l'arrêt")
	}
}

func TestAssessMaintenanceSilencesFanAlertsOnly(t *testing.T) {
	in := input()
	in.Maintenance = true
	in.Devs = []device{fan(1, 0), fan(2, 0), fan(3, 0), fan(15, 0), pr(13, 43), pr(14, 23)}
	got := keys(assess(in, defaultLimits()))
	if strings.Contains(got, "corsair") {
		t.Fatalf("en maintenance, aucune alerte de ventilateur: %s", got)
	}
	if !strings.Contains(got, "probe-NAS:crit") {
		t.Fatalf("les alertes de température restent actives: %s", got)
	}
}

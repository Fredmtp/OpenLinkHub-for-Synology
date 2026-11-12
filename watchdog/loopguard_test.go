package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAutoStopMarkerRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arret-auto.json")
	if _, ok := readAutoStop(path); ok {
		t.Fatal("pas de marqueur: aucun arrêt connu")
	}
	at := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	if err := writeAutoStop(path, at, "ventilation perdue"); err != nil {
		t.Fatal(err)
	}
	s, ok := readAutoStop(path)
	if !ok || !s.At.Equal(at) || s.Reason != "ventilation perdue" {
		t.Fatalf("relu: %+v %v", s, ok)
	}
}

func TestAutoStopCorruptMarkerIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arret-auto.json")
	os.WriteFile(path, []byte("{pas du json"), 0o644)
	if _, ok := readAutoStop(path); ok {
		t.Fatal("un marqueur illisible ne doit pas suspendre l'arrêt")
	}
	os.WriteFile(path, []byte(`{"raison":"x"}`), 0o644)
	if _, ok := readAutoStop(path); ok {
		t.Fatal("un marqueur sans date ne doit pas suspendre l'arrêt")
	}
}

func TestGuardUntilWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arret-auto.json")
	at := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	writeAutoStop(path, at, "x")
	if _, until, ok := guardUntil(path, 6*time.Hour, at.Add(5*time.Hour)); !ok || !until.Equal(at.Add(6*time.Hour)) {
		t.Fatalf("garde attendue 5 h après l'arrêt: %v %v", until, ok)
	}
	if _, _, ok := guardUntil(path, 6*time.Hour, at.Add(6*time.Hour+time.Second)); ok {
		t.Fatal("la garde doit se terminer après 6 h")
	}
	if _, _, ok := guardUntil("", 6*time.Hour, at); ok {
		t.Fatal("sans fichier configuré, pas de garde")
	}
	if _, _, ok := guardUntil(path, 0, at); ok {
		t.Fatal("durée nulle: garde désactivée")
	}
}

func TestGuardSuspendsOnlyTheVentilationShutdown(t *testing.T) {
	m := newMon()
	m.fanGuardUntil = at(300 + 6*3600)
	bad := meuble(0, 0, 0, 620) // perte totale
	for s := 300; s < 300+3600; s += 30 {
		if r := m.evaluate(bad, at(s)); r != "" {
			t.Fatalf("garde active: pas de nouvel arrêt par la ventilation: %s", r)
		}
		if m.fanStop {
			t.Fatal("aucun arrêt demandé: fanStop doit rester faux")
		}
	}
	// La garde expirée, la perte totale persistante éteint de nouveau.
	if r := m.evaluate(bad, at(300+6*3600+1)); r == "" || !m.fanStop {
		t.Fatalf("garde expirée: arrêt attendu (%q, fanStop=%v)", r, m.fanStop)
	}
}

func TestGuardDoesNotSuspendTemperatureShutdowns(t *testing.T) {
	m := newMon()
	m.fanGuardUntil = at(300 + 6*3600)
	// Disques: nasCheck ne regarde jamais la garde.
	if r := m.nasCheck(diskSnap(46, 1000), at(1000)); r != "" {
		t.Fatalf("arrêt immédiat: %s", r)
	}
	if r := m.nasCheck(diskSnap(46, 1061), at(1061)); r == "" {
		t.Fatal("un disque à 45 °C ou plus doit éteindre le NAS même sous garde")
	}
	// Sondes du meuble: l'arrêt vient de la température, pas de la ventilation (fanStop faux).
	m2 := newMon()
	m2.fanGuardUntil = at(300 + 6*3600)
	hot := append(meuble(770, 770, 770, 620)[:4], pr(13, 46), pr(14, 23))
	m2.evaluate(hot, at(300))
	if r := m2.evaluate(hot, at(481)); r == "" || m2.fanStop {
		t.Fatalf("sonde trop chaude: arrêt attendu sans marqueur de ventilation (%q, fanStop=%v)", r, m2.fanStop)
	}
}

func TestFanStopFlagSetOnlyForVentilationReason(t *testing.T) {
	m := newMon()
	bad := meuble(0, 0, 0, 620)
	m.evaluate(bad, at(300))
	if r := m.evaluate(bad, at(300+int(*fanLostFor/time.Second)+1)); r == "" || !m.fanStop {
		t.Fatalf("perte totale: arrêt avec fanStop attendu (%q, %v)", r, m.fanStop)
	}
	m.evaluate(meuble(770, 770, 770, 620), at(5000))
	if m.fanStop {
		t.Fatal("fanStop doit être remis à zéro à chaque évaluation")
	}
}

func TestGuardClearsWhenVentilationReturns(t *testing.T) {
	m := newMon()
	m.fanGuardUntil = at(300 + 6*3600)
	bad := meuble(0, 0, 0, 620)
	m.evaluate(bad, at(300))
	m.evaluate(bad, at(300+int(*fanLostFor/time.Second)+1))
	if !m.warned["loopguard"] {
		t.Fatal("l'avertissement de garde doit être posé")
	}
	m.evaluate(meuble(770, 770, 770, 620), at(2000))
	if m.warned["loopguard"] {
		t.Fatal("l'avertissement de garde doit être retiré quand la ventilation revient")
	}
}

func TestAutoStopMessage(t *testing.T) {
	at := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	msg := autoStopMessage(autoStop{At: at, Reason: "tous les ventilateurs d'extraction arrêtés"}, at.Add(6*time.Hour), false, "")
	if !strings.Contains(msg.Short, "pas de nouvel arrêt par la ventilation avant 09:00") || !strings.Contains(msg.Body, "45 °C") {
		t.Fatalf("message inattendu: %q / %q", msg.Short, msg.Body)
	}
}

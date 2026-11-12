package main

import (
	"strings"
	"testing"
	"time"
)

// Rappel lumineux de la maintenance (décision du 28/10/2026): pendant la maintenance, les 4 ventilateurs
// prennent le profil « maintenance » (par exemple bleu fixe); une alerte l'emporte toujours.

func newBancMaint(t *testing.T) *bancLED {
	b := newBancLED(t)
	b.e.maintProfile = "maintenance"
	return b
}

func (b *bancLED) profiles() map[int]string {
	out := map[int]string{}
	for ch, p := range b.profile {
		out[ch] = p
	}
	return out
}

func TestLEDMaintenanceColorsAllFourAndRestores(t *testing.T) {
	b := newBancMaint(t)
	b.e.tickM(nil, true, time.Unix(3000, 0))
	for _, ch := range []int{1, 2, 3, 15} {
		if b.profile[ch] != "maintenance" {
			t.Fatalf("canal %d: %q, attendu le profil de maintenance", ch, b.profile[ch])
		}
	}
	// fin de la maintenance: tout revient au profil d'origine
	b.e.tickM(nil, false, time.Unix(3001, 0))
	for _, ch := range []int{1, 2, 3, 15} {
		if b.profile[ch] != "probe-temperature" {
			t.Fatalf("canal %d après la maintenance: %q", ch, b.profile[ch])
		}
	}
}

func TestLEDMaintenanceNoCallsWhenNothingChanges(t *testing.T) {
	b := newBancMaint(t)
	b.e.tickM(nil, true, time.Unix(3000, 0))
	n := len(b.calls)
	for i := 1; i < 10; i++ {
		b.e.tickM(nil, true, time.Unix(3000+int64(i), 0))
	}
	if len(b.calls) != n {
		t.Fatalf("la couleur de maintenance est posée une fois, sans écritures répétées: %v", b.calls)
	}
}

func TestLEDAlertWinsOverMaintenanceThenGoesBack(t *testing.T) {
	b := newBancMaint(t)
	b.e.tickM(nil, true, time.Unix(3000, 0))
	b.e.tickM([]finding{ledCrit("disk-temp-sata7")}, true, time.Unix(3001, 0)) // zone du canal 2
	if b.profile[2] != "circle" {
		t.Fatalf("le ventilateur de la zone doit passer en alerte: %q", b.profile[2])
	}
	for _, ch := range []int{1, 3, 15} {
		if b.profile[ch] != "maintenance" {
			t.Fatalf("canal %d: %q, la maintenance continue pour les autres", ch, b.profile[ch])
		}
	}
	// l'alerte disparaît, la maintenance continue: le canal 2 revient à la couleur de maintenance
	b.e.tickM(nil, true, time.Unix(3002, 0))
	if b.profile[2] != "maintenance" {
		t.Fatalf("canal 2 après l'alerte: %q, attendu la couleur de maintenance", b.profile[2])
	}
	// fin de maintenance: tout revient au profil d'origine, pas à « circle » ni « maintenance »
	b.e.tickM(nil, false, time.Unix(3003, 0))
	for ch, p := range b.profiles() {
		if p != "probe-temperature" {
			t.Fatalf("canal %d: %q après tout", ch, p)
		}
	}
}

func TestLEDMaintenanceDisabledWhenProfileEmpty(t *testing.T) {
	b := newBancLED(t) // pas de profil de maintenance
	b.e.tickM(nil, true, time.Unix(3000, 0))
	if len(b.calls) != 0 || b.fetches != 0 {
		t.Fatalf("sans profil de maintenance, aucune écriture: %v", b.calls)
	}
}

func TestLEDMaintenanceMissingProfileBacksOff(t *testing.T) {
	b := newBancMaint(t)
	b.failSet[1], b.failSet[2], b.failSet[3], b.failSet[15] = true, true, true, true // profil inexistant: refusé
	b.e.tickM(nil, true, time.Unix(3000, 0))
	n := len(b.calls)
	b.e.tickM(nil, true, time.Unix(3005, 0))
	if len(b.calls) != n {
		t.Fatalf("après un refus, pas de nouvel essai avant 20 s: %v", b.calls)
	}
}

func TestMaintenanceReminderMessage(t *testing.T) {
	msg := maintenanceReminder(time.Date(2026, 10, 28, 15, 30, 0, 0, time.UTC), false, "")
	if msg.Priority != 3 || msg.Short == "" || msg.Body == "" {
		t.Fatalf("%+v", msg)
	}
	if want := "jusqu'à 15:30"; !strings.Contains(msg.Short, want) {
		t.Fatalf("%q ne contient pas %q", msg.Short, want)
	}
}

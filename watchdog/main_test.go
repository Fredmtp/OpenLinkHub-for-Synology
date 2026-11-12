package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func newMon() *monitor {
	return &monitor{
		started: time.Unix(0, 0),
		seenFan: map[int]bool{},
		fanDown: map[int]bool{},
		warned:  map[string]bool{},
		probes: []*probe{
			{label: "NAS", channel: 13, sustained: 45, instant: 50},
			{label: "QNAP", channel: 14, sustained: 45, instant: 50},
		},
	}
}

func fan(ch, rpm int) device {
	return device{ChannelID: ch, Description: "Fan", HasSpeed: true, RPM: rpm}
}

func pr(ch int, t float64) device {
	return device{ChannelID: ch, IsTemperatureProbe: true, Temperature: t}
}

func at(s int) time.Time { return time.Unix(int64(s), 0) }

// meuble assemble une liste complète: extraction (canaux 1, 2, 3 aux vitesses données), admission
// (canal 15) et les deux sondes à 27 et 23 °C.
func meuble(e1, e2, e3, in int) []device {
	return []device{fan(1, e1), fan(2, e2), fan(3, e3), fan(15, in), pr(13, 27), pr(14, 23)}
}

// withMaintenance crée un fichier de veille valide jusqu'à `until` et le branche sur -maintenance.
func withMaintenance(t *testing.T, until time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "maintenance")
	if err := os.WriteFile(path, []byte(strconv.FormatInt(until.Unix(), 10)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := *maintFile
	*maintFile = path
	t.Cleanup(func() { *maintFile = old })
}

func TestNominal(t *testing.T) {
	m := newMon()
	if r := m.evaluate([]device{fan(1, 770), fan(2, 770), pr(13, 27), pr(14, 23)}, at(300)); r != "" {
		t.Fatalf("arrêt inattendu: %s", r)
	}
}

func TestAllFansDeadShutsDownAfterDelay(t *testing.T) {
	m := newMon()
	devs := []device{fan(1, 0), fan(2, 0), fan(3, 0), pr(13, 27), pr(14, 23)}
	if r := m.evaluate(devs, at(300)); r != "" {
		t.Fatalf("arrêt trop tôt: %s", r)
	}
	if r := m.evaluate(devs, at(1000)); r != "" {
		t.Fatalf("arrêt avant 15 min: %s", r)
	}
	if r := m.evaluate(devs, at(1201)); r == "" {
		t.Fatal("arrêt attendu après 15 min sans ventilateur")
	}
}

func TestNoFanListedIsNotAProvenFailure(t *testing.T) {
	m := newMon()
	for _, devs := range [][]device{{pr(13, 27), pr(14, 23)}, {}} {
		for s := 300; s < 1200; s += 10 {
			if r := m.evaluate(devs, at(s)); r != "" {
				t.Fatalf("une liste vide (mesure impossible) ne doit pas éteindre le NAS: %s", r)
			}
		}
	}
}

func TestFanListedAtZeroRPMStillShutsDownAfterBlindPeriod(t *testing.T) {
	m := newMon()
	m.evaluate([]device{}, at(300))
	devs := []device{fan(1, 0), fan(2, 0), fan(3, 0)}
	m.evaluate(devs, at(400))
	if r := m.evaluate(devs, at(1301)); r == "" {
		t.Fatal("ventilateurs listés à 0 tr/min doivent éteindre le NAS")
	}
}

func TestCpuCheck(t *testing.T) {
	m := newMon()
	if r := m.cpuCheck(60, at(0)); r != "" {
		t.Fatalf("arrêt à 60 °C: %s", r)
	}
	if r := m.cpuCheck(90, at(10)); r != "" {
		t.Fatalf("arrêt trop tôt: %s", r)
	}
	if r := m.cpuCheck(90, at(100)); r != "" {
		t.Fatalf("arrêt avant 3 min: %s", r)
	}
	if r := m.cpuCheck(60, at(150)); r != "" {
		t.Fatalf("arrêt après refroidissement: %s", r)
	}
	m.cpuCheck(90, at(200))
	if r := m.cpuCheck(90, at(381)); r == "" {
		t.Fatal("processeur trop chaud trop longtemps doit éteindre le NAS")
	}
}

func TestOneFanDownDoesNotShutDown(t *testing.T) {
	m := newMon()
	m.evaluate([]device{fan(1, 770), fan(2, 770), fan(3, 770), pr(13, 27), pr(14, 23)}, at(300))
	devs := []device{fan(1, 770), fan(2, 0), fan(3, 770), pr(13, 27), pr(14, 23)}
	for s := 310; s < 900; s += 10 {
		if r := m.evaluate(devs, at(s)); r != "" {
			t.Fatalf("un seul ventilateur HS ne doit pas éteindre le NAS: %s", r)
		}
	}
}

func TestGracePeriod(t *testing.T) {
	m := newMon()
	devs := []device{fan(1, 0), pr(13, 60), pr(14, 60)}
	for s := 0; s < 100; s += 10 {
		if r := m.evaluate(devs, at(s)); r != "" {
			t.Fatalf("arrêt pendant le délai de grâce: %s", r)
		}
	}
}

func TestInstantNeedsTwoReadings(t *testing.T) {
	m := newMon()
	ok := append(meuble(770, 770, 770, 620)[:4], pr(13, 30), pr(14, 23))
	hot := append(meuble(770, 770, 770, 620)[:4], pr(13, 51), pr(14, 23))
	if r := m.evaluate(hot, at(300)); r != "" {
		t.Fatalf("un pic isolé ne doit pas éteindre: %s", r)
	}
	if r := m.evaluate(ok, at(310)); r != "" {
		t.Fatalf("arrêt inattendu: %s", r)
	}
	m.evaluate(hot, at(320))
	if r := m.evaluate(hot, at(330)); r == "" {
		t.Fatal("deux mesures au-dessus du seuil immédiat doivent éteindre")
	}
}

func TestSustainedHeat(t *testing.T) {
	m := newMon()
	hot := append(meuble(770, 770, 770, 620)[:4], pr(13, 30), pr(14, 46))
	if r := m.evaluate(hot, at(300)); r != "" {
		t.Fatalf("arrêt trop tôt: %s", r)
	}
	if r := m.evaluate(hot, at(400)); r != "" {
		t.Fatalf("arrêt avant 3 min: %s", r)
	}
	if r := m.evaluate(hot, at(481)); r == "" {
		t.Fatal("chaleur prolongée sur la sonde QNAP doit éteindre")
	}
}

func TestHeatResetsWhenCooled(t *testing.T) {
	m := newMon()
	hot := append(meuble(770, 770, 770, 620)[:4], pr(13, 46), pr(14, 23))
	cool := append(meuble(770, 770, 770, 620)[:4], pr(13, 40), pr(14, 23))
	m.evaluate(hot, at(300))
	m.evaluate(cool, at(400))
	if r := m.evaluate(hot, at(500)); r != "" {
		t.Fatalf("le compteur aurait dû repartir de zéro: %s", r)
	}
}

func TestDisconnectedProbeIgnored(t *testing.T) {
	m := newMon()
	devs := append(meuble(770, 770, 770, 620)[:4], pr(13, 0), pr(14, 23))
	for s := 300; s < 900; s += 10 {
		if r := m.evaluate(devs, at(s)); r != "" {
			t.Fatalf("une sonde illisible ne doit pas éteindre: %s", r)
		}
	}
}

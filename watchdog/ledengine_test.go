package main

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// banc d'essai du moteur de LED: un faux Hub dont les ventilateurs sont en « probe-temperature ».
type bancLED struct {
	e       *ledEngine
	calls   []string // « canal:profil »
	fetches int
	failSet map[int]bool
	profile map[int]string // profil courant de chaque canal, tenu à jour par set
}

func newBancLED(t *testing.T) *bancLED {
	b := &bancLED{failSet: map[int]bool{}, profile: map[int]string{1: "probe-temperature", 2: "probe-temperature", 3: "probe-temperature", 15: "probe-temperature"}}
	zones, _ := parseZones("1-3:1,4-5:3,6-8:2")
	b.e = &ledEngine{profile: "circle", normal: "probe-temperature", order: []int{1, 3, 2}, zones: zones,
		extraction: []int{1, 2, 3}, all: []int{1, 2, 3, 15}, alt: 3 * time.Second,
		statePath: filepath.Join(t.TempDir(), "leds-etat.json"), orig: map[int]string{},
		fetch: func() (hubFans, error) {
			b.fetches++
			hf := hubFans{HubID: "HUB"}
			for _, ch := range []int{1, 2, 3, 15} {
				hf.Fans = append(hf.Fans, fanLED{Channel: ch, Profile: b.profile[ch]})
			}
			return hf, nil
		},
		set: func(hub string, ch int, p string) error {
			b.calls = append(b.calls, itoa(ch)+":"+p)
			if b.failSet[ch] {
				return errors.New("refusé")
			}
			b.profile[ch] = p
			return nil
		}}
	return b
}

func itoa(n int) string { return strconv.Itoa(n) }

func (b *bancLED) lit() []int {
	var out []int
	for ch, p := range b.profile {
		if p == "circle" {
			out = append(out, ch)
		}
	}
	sort.Ints(out)
	return out
}

func ledCrit(key string) finding { return finding{Key: key, Level: levelCrit} }
func ledWarn(key string) finding { return finding{Key: key, Level: levelWarn} }

func eq(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseZones(t *testing.T) {
	z, err := parseZones("1-3:1,4-5:3,6-8:2")
	if err != nil || len(z) != 3 {
		t.Fatalf("%v %v", z, err)
	}
	for bay, want := range map[int]int{1: 1, 3: 1, 4: 3, 5: 3, 6: 2, 8: 2} {
		if ch, ok := zoneChannel(z, bay); !ok || ch != want {
			t.Errorf("baie %d -> canal %d (%v), attendu %d", bay, ch, ok, want)
		}
	}
	if _, ok := zoneChannel(z, 9); ok {
		t.Error("la baie 9 n'existe pas")
	}
	for _, bad := range []string{"", "1-3", "x:1", "3-1:1", "0-2:1"} {
		if _, err := parseZones(bad); err == nil {
			t.Errorf("%q devrait être refusé", bad)
		}
	}
}

func TestLEDNothingToDoMakesNoCall(t *testing.T) {
	b := newBancLED(t)
	b.e.tick(nil, time.Unix(3000, 0))
	b.e.tick([]finding{ledWarn("disk-temp-sata2"), ledWarn("probe-NAS"), ledCrit("disk-bad-sata1"), ledCrit("probe-NAS-missing")}, time.Unix(3003, 0))
	if len(b.calls) != 0 || b.fetches != 0 {
		t.Fatalf("aucun appel attendu sans anomalie critique: %v (lectures %d)", b.calls, b.fetches)
	}
}

func TestLEDDiskZones(t *testing.T) {
	cases := map[string]int{"disk-temp-sata1": 1, "disk-temp-sata3": 1, "disk-temp-sata4": 3, "disk-temp-sata5": 3, "disk-temp-sata6": 2, "disk-temp-sata8": 2}
	for key, ch := range cases {
		b := newBancLED(t)
		b.e.tick([]finding{ledCrit(key)}, time.Unix(3000, 0))
		if got := b.lit(); len(got) != 1 || got[0] != ch {
			t.Errorf("%s: allumés %v, attendu le canal %d seul", key, got, ch)
		}
		b.e.tick(nil, time.Unix(3001, 0))
		if got := b.lit(); len(got) != 0 {
			t.Errorf("%s: après retour à la normale, encore allumés %v", key, got)
		}
	}
}

func TestLEDUnknownBayLightsAllExtractors(t *testing.T) {
	b := newBancLED(t)
	b.e.tick([]finding{ledCrit("disk-temp-nvme1")}, time.Unix(3000, 0))
	if got := b.lit(); !eq(got, []int{1, 2, 3}) {
		t.Fatalf("baie inconnue: les trois extracteurs, pas l'admission: %v", got)
	}
}

func TestLEDVentilationLostOrDegradedLightsAllFour(t *testing.T) {
	for _, key := range []string{"corsair-extraction", "corsair-all"} {
		b := newBancLED(t)
		b.e.tick([]finding{ledCrit(key)}, time.Unix(3000, 0))
		if got := b.lit(); !eq(got, []int{1, 2, 3, 15}) {
			t.Errorf("%s: les 4 ventilateurs attendus: %v", key, got)
		}
		b.e.tick(nil, time.Unix(3001, 0))
		if got := b.lit(); len(got) != 0 {
			t.Errorf("%s: encore allumés %v", key, got)
		}
	}
}

func TestLEDSingleFanDownLightsOnlyThatFan(t *testing.T) {
	b := newBancLED(t)
	b.e.tick([]finding{ledWarn("corsair-fan-2")}, time.Unix(3000, 0))
	if got := b.lit(); !eq(got, []int{2}) {
		t.Fatalf("seul le canal 2: %v", got)
	}
	b = newBancLED(t)
	b.e.tick([]finding{ledWarn("corsair-fan-15")}, time.Unix(3000, 0))
	if got := b.lit(); !eq(got, []int{15}) {
		t.Fatalf("l'admission en panne s'allume seule: %v", got)
	}
}

// Le NAS chauffe: les trois extracteurs alternent de gauche à droite (canaux 1, 3, 2), l'admission jamais.
func TestLEDNASHeatAlternatesLeftCenterRight(t *testing.T) {
	for _, key := range []string{"cpu-temp", "board-temp", "probe-NAS", "probe-QNAP"} {
		b := newBancLED(t)
		t0 := time.Unix(3*1000, 0)
		var seq []int
		for i := 0; i < 7; i++ {
			b.e.tick([]finding{ledCrit(key)}, t0.Add(time.Duration(i)*3*time.Second))
			got := b.lit()
			if len(got) != 1 {
				t.Fatalf("%s pas %d: un seul ventilateur à la fois, allumés %v", key, i, got)
			}
			seq = append(seq, got[0])
		}
		for i := 1; i < len(seq); i++ {
			next := map[int]int{1: 3, 3: 2, 2: 1}[seq[i-1]]
			if seq[i] != next {
				t.Fatalf("%s: ordre %v, attendu 1 → 3 → 2 → 1", key, seq)
			}
		}
		for _, ch := range seq {
			if ch == 15 {
				t.Fatalf("%s: l'admission ne doit jamais s'allumer", key)
			}
		}
	}
}

func TestLEDNoChangeWithinTheSameStep(t *testing.T) {
	b := newBancLED(t)
	t0 := time.Unix(3*1000, 0)
	b.e.tick([]finding{ledCrit("cpu-temp")}, t0)
	n := len(b.calls)
	b.e.tick([]finding{ledCrit("cpu-temp")}, t0.Add(time.Second))
	b.e.tick([]finding{ledCrit("cpu-temp")}, t0.Add(2*time.Second))
	if len(b.calls) != n {
		t.Fatalf("aucune écriture à l'intérieur d'un même pas de 3 s: %v", b.calls)
	}
}

func TestLEDDiskAndNASHeatTogether(t *testing.T) {
	b := newBancLED(t)
	t0 := time.Unix(3*1000, 0)
	for i := 0; i < 3; i++ {
		b.e.tick([]finding{ledCrit("disk-temp-sata7"), ledCrit("cpu-temp")}, t0.Add(time.Duration(i)*3*time.Second))
		got := b.lit()
		has2 := false
		for _, ch := range got {
			if ch == 2 {
				has2 = true
			}
		}
		if !has2 {
			t.Fatalf("le ventilateur de la zone du disque (canal 2) reste allumé: %v", got)
		}
		if len(got) > 2 {
			t.Fatalf("au plus la zone et un extracteur en alternance: %v", got)
		}
	}
}

func TestLERestoresTheOriginalProfileNotAFixedOne(t *testing.T) {
	b := newBancLED(t)
	b.profile[3] = "rainbow" // profil d'origine particulier
	b.e.tick([]finding{ledCrit("disk-temp-sata4")}, time.Unix(3000, 0))
	b.e.tick(nil, time.Unix(3001, 0))
	if b.profile[3] != "rainbow" {
		t.Fatalf("le profil d'origine doit être remis: %q", b.profile[3])
	}
}

func TestLEDAlreadyInAlertProfileFallsBackToNormal(t *testing.T) {
	b := newBancLED(t)
	b.profile[1] = "circle" // resté en alerte (arrêt brutal sans fichier d'état)
	b.e.tick([]finding{ledCrit("disk-temp-sata1")}, time.Unix(3000, 0))
	b.e.tick(nil, time.Unix(3001, 0))
	if b.profile[1] != "probe-temperature" {
		t.Fatalf("profil normal par défaut attendu: %q", b.profile[1])
	}
}

func TestLEDFailureIsRetriedAndNeverCrashes(t *testing.T) {
	b := newBancLED(t)
	b.failSet[1] = true
	b.e.tick([]finding{ledCrit("disk-temp-sata1")}, time.Unix(3000, 0))
	if len(b.lit()) != 0 || len(b.e.orig) != 0 {
		t.Fatalf("un échec ne doit rien marquer comme allumé: %v %v", b.lit(), b.e.orig)
	}
	b.failSet[1] = false
	n := len(b.calls)
	b.e.tick([]finding{ledCrit("disk-temp-sata1")}, time.Unix(3001, 0))
	if len(b.calls) != n {
		t.Fatalf("après un échec, OpenLinkHub est laissé tranquille quelques secondes: %v", b.calls)
	}
	b.e.tick([]finding{ledCrit("disk-temp-sata1")}, time.Unix(3030, 0))
	if got := b.lit(); !eq(got, []int{1}) {
		t.Fatalf("nouvel essai attendu: %v", got)
	}
}

func TestLEDStateFileWrittenAndRemoved(t *testing.T) {
	b := newBancLED(t)
	b.e.tick([]finding{ledCrit("disk-temp-sata4")}, time.Unix(3000, 0))
	raw, err := os.ReadFile(b.e.statePath)
	if err != nil || !strings.Contains(string(raw), `"3":"probe-temperature"`) {
		t.Fatalf("fichier d'état attendu avec le canal 3: %q %v", raw, err)
	}
	b.e.tick(nil, time.Unix(3001, 0))
	if _, err := os.Stat(b.e.statePath); err == nil {
		t.Fatal("le fichier d'état doit disparaître quand plus rien n'est allumé")
	}
}

func TestLEDRecoveryAfterBrutalStop(t *testing.T) {
	b := newBancLED(t)
	b.e.tick([]finding{ledCrit("corsair-extraction")}, time.Unix(3000, 0))
	// la surveillance s'arrête brutalement: un nouveau moteur reprend le fichier d'état
	state := b.e.statePath
	b2 := newBancLED(t)
	b2.e.statePath = state
	b2.profile = b.profile // mêmes ventilateurs, encore en alerte
	b2.e.recover()
	if got := b2.lit(); len(got) != 0 {
		t.Fatalf("après reprise, plus rien ne doit rester allumé: %v", got)
	}
	if _, err := os.Stat(state); err == nil {
		t.Fatal("le fichier d'état doit être supprimé après la reprise")
	}
}

func TestLEDRestoreAllOnStop(t *testing.T) {
	b := newBancLED(t)
	b.e.tick([]finding{ledCrit("corsair-all")}, time.Unix(3000, 0))
	b.e.restoreAll()
	if got := b.lit(); len(got) != 0 {
		t.Fatalf("restoreAll doit tout remettre: %v", got)
	}
}

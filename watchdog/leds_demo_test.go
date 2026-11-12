package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestParseChannels(t *testing.T) {
	got, err := parseChannels("1, 3,2")
	if err != nil || len(got) != 3 || got[0] != 1 || got[1] != 3 || got[2] != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := parseChannels("1,x"); err == nil {
		t.Fatal("un canal invalide doit être refusé")
	}
}

func TestDemoAlternateLightsOneExtractorAtATimeLeftToRight(t *testing.T) {
	f := newFauxOLH(t)
	var out bytes.Buffer
	err := demoAlternate(f.srv.URL, "circle", []int{1, 3, 2}, []time.Duration{2 * time.Second}, 2, &out, always(true))
	if err != nil {
		t.Fatal(err)
	}
	// Deux tours: chaque ventilateur s'allume puis reprend son profil avant le suivant.
	want := "1:circle 1:probe-temperature 3:circle 3:probe-temperature 2:circle 2:probe-temperature " +
		"1:circle 1:probe-temperature 3:circle 3:probe-temperature 2:circle 2:probe-temperature"
	if got := strings.Join(f.calls, " "); got != want {
		t.Fatalf("appels:\n %s\nattendu:\n %s", got, want)
	}
	if !strings.Contains(out.String(), "toutes les 2s") || !strings.Contains(out.String(), "tous les profils d'origine sont remis") {
		t.Fatalf("sortie: %s", out.String())
	}
}

func TestDemoAlternateNeverLightsTwoAtOnce(t *testing.T) {
	f := newFauxOLH(t)
	var out bytes.Buffer
	if err := demoAlternate(f.srv.URL, "circle", []int{1, 3, 2}, []time.Duration{time.Second}, 1, &out, always(true)); err != nil {
		t.Fatal(err)
	}
	lit := map[string]bool{}
	for _, c := range f.calls {
		p := strings.SplitN(c, ":", 2)
		if p[1] == "circle" {
			lit[p[0]] = true
		} else {
			delete(lit, p[0])
		}
		if len(lit) > 1 {
			t.Fatalf("deux ventilateurs allumés en même temps après %s: %v", c, lit)
		}
	}
	if len(lit) != 0 {
		t.Fatalf("un ventilateur est resté allumé: %v", lit)
	}
}

func TestDemoAlternateInterruptionRestores(t *testing.T) {
	f := newFauxOLH(t)
	var out bytes.Buffer
	n := 0
	wait := func(time.Duration) bool { n++; return n < 2 } // interrompu pendant le 2e ventilateur
	if err := demoAlternate(f.srv.URL, "circle", []int{1, 3, 2}, []time.Duration{time.Second, 4 * time.Second}, 3, &out, wait); err != nil {
		t.Fatal(err)
	}
	last := f.calls[len(f.calls)-1]
	if last != "3:probe-temperature" {
		t.Fatalf("le ventilateur allumé à l'interruption doit être remis en l'état, dernier appel: %s (tous: %v)", last, f.calls)
	}
	if !strings.Contains(out.String(), "Interrompu") {
		t.Fatalf("sortie: %s", out.String())
	}
}

func TestDemoAlternateRefusesAnUnknownChannel(t *testing.T) {
	f := newFauxOLH(t)
	var out bytes.Buffer
	err := demoAlternate(f.srv.URL, "circle", []int{1, 9}, []time.Duration{time.Second}, 1, &out, always(true))
	if err == nil || !strings.Contains(err.Error(), "canal 9") {
		t.Fatalf("erreur attendue: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("rien ne doit être modifié: %v", f.calls)
	}
}

func TestDemoAlternateReportsARestoreFailure(t *testing.T) {
	f := newFauxOLH(t)
	f.failNext["3:probe-temperature"] = true
	var out bytes.Buffer
	err := demoAlternate(f.srv.URL, "circle", []int{1, 3, 2}, []time.Duration{time.Second}, 1, &out, always(true))
	if err == nil || !strings.Contains(err.Error(), "à remettre à la main") {
		t.Fatalf("erreur attendue: %v", err)
	}
	if !strings.Contains(out.String(), "NON remis") || !strings.Contains(out.String(), "canal 3") {
		t.Fatalf("la sortie doit nommer le ventilateur non remis: %s", out.String())
	}
}

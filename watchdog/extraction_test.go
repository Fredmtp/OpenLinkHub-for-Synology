package main

import (
	"strings"
	"testing"
	"time"
)

// ---- règle d'arrêt de la ventilation (décision du 28/10/2026) ----
// Extraction = canaux 1, 2, 3; admission = canal 15. Seule la PERTE TOTALE (aucun ventilateur en
// rotation, ou les trois extracteurs arrêtés ou absents, même si l'admission tourne) éteint le NAS,
// après 15 min. Deux extracteurs arrêtés: ventilation dégradée (alerte, l'arrêt ne vient que de la
// température). Un seul extracteur ou l'admission seule: alerte simple.

// shutsAfterDelay vérifie qu'une liste ne provoque aucun arrêt avant 15 min, puis un arrêt.
func shutsAfterDelay(t *testing.T, devs []device) {
	t.Helper()
	m := newMon()
	if r := m.evaluate(devs, at(300)); r != "" {
		t.Fatalf("arrêt trop tôt: %s", r)
	}
	if r := m.evaluate(devs, at(300+int(*fanLostFor/time.Second)-60)); r != "" {
		t.Fatalf("arrêt avant le délai: %s", r)
	}
	if r := m.evaluate(devs, at(300+int(*fanLostFor/time.Second)+1)); r == "" {
		t.Fatal("arrêt attendu après le délai")
	}
}

// staysUp vérifie qu'une liste n'éteint jamais le NAS, même après un long moment.
func staysUp(t *testing.T, devs []device) {
	t.Helper()
	m := newMon()
	for s := 300; s < 3000; s += 10 {
		if r := m.evaluate(devs, at(s)); r != "" {
			t.Fatalf("arrêt inattendu: %s", r)
		}
	}
}

func TestDefaultLostDelayIsFifteenMinutes(t *testing.T) {
	if *fanLostFor != 15*time.Minute {
		t.Fatalf("délai de perte totale: %s, attendu 15 min", *fanLostFor)
	}
}

func TestTwoExtractionFansDownDoNotShutDown(t *testing.T) {
	for _, devs := range [][]device{meuble(770, 0, 0, 620), meuble(0, 770, 0, 620), meuble(0, 0, 770, 620)} {
		staysUp(t, devs)
	}
}

func TestThreeExtractionFansDownShutDownEvenIfIntakeRuns(t *testing.T) {
	shutsAfterDelay(t, meuble(0, 0, 0, 620))
}

func TestAllFourFansDownShutDown(t *testing.T) {
	shutsAfterDelay(t, meuble(0, 0, 0, 0))
}

func TestOneExtractionFanDownIsTolerated(t *testing.T) {
	for _, devs := range [][]device{meuble(0, 770, 770, 620), meuble(770, 0, 770, 620), meuble(770, 770, 0, 620)} {
		staysUp(t, devs)
	}
}

func TestIntakeFanAloneDownIsNotCritical(t *testing.T) {
	staysUp(t, meuble(770, 770, 770, 0))
}

func TestIntakeDownPlusOneExtractionDownIsStillTolerated(t *testing.T) {
	staysUp(t, meuble(0, 770, 770, 0))
}

func TestAbsentExtractionFansCountAsStopped(t *testing.T) {
	// Les trois extracteurs absents de la liste: on ne peut pas savoir s'ils tournent => perte totale.
	shutsAfterDelay(t, []device{fan(15, 620), pr(13, 27), pr(14, 23)})
	// Deux absents: ventilation dégradée, pas d'arrêt par les ventilateurs.
	staysUp(t, []device{fan(1, 770), fan(15, 620), pr(13, 27), pr(14, 23)})
	// Un seul absent: toléré.
	staysUp(t, []device{fan(1, 770), fan(2, 770), fan(15, 620), pr(13, 27), pr(14, 23)})
}

func TestIntakeAbsentIsNotCritical(t *testing.T) {
	staysUp(t, []device{fan(1, 770), fan(2, 770), fan(3, 770), pr(13, 27), pr(14, 23)})
}

func TestLostRuleStillWorksWhenDegradedRuleIsDisabled(t *testing.T) {
	old := lim.ExtractionStop
	lim.ExtractionStop = 0
	defer func() { lim.ExtractionStop = old }()
	staysUp(t, meuble(0, 0, 770, 620))
	shutsAfterDelay(t, meuble(0, 0, 0, 620))
}

func TestLostRecoveryResetsCountdown(t *testing.T) {
	m := newMon()
	bad, good := meuble(0, 0, 0, 620), meuble(770, 770, 770, 620)
	m.evaluate(bad, at(300))
	m.evaluate(bad, at(900))
	m.evaluate(good, at(950))
	if r := m.evaluate(bad, at(1300)); r != "" {
		t.Fatalf("le compte à rebours aurait dû repartir de zéro: %s", r)
	}
}

func TestMaintenanceSuspendsLostRule(t *testing.T) {
	withMaintenance(t, at(100000))
	staysUp(t, meuble(0, 0, 0, 0))
}

func TestVentilationState(t *testing.T) {
	l := defaultLimits()
	cases := []struct {
		name string
		devs []device
		want ventState
	}{
		{"normale", meuble(770, 770, 770, 620), ventOK},
		{"un extracteur", meuble(0, 770, 770, 620), ventPartial},
		{"admission seule", meuble(770, 770, 770, 0), ventPartial},
		{"deux extracteurs", meuble(0, 0, 770, 620), ventDegraded},
		{"trois extracteurs, admission en marche", meuble(0, 0, 0, 620), ventLost},
		{"quatre", meuble(0, 0, 0, 0), ventLost},
		{"deux absents", []device{fan(1, 770), fan(15, 620)}, ventDegraded},
		{"trois absents", []device{fan(15, 620)}, ventLost},
	}
	for _, c := range cases {
		if got, _, ok := ventilationState(c.devs, l); !ok || got != c.want {
			t.Errorf("%s: état %v (mesure possible: %v), attendu %v", c.name, got, ok, c.want)
		}
	}
	if _, _, ok := ventilationState([]device{pr(13, 27)}, l); ok {
		t.Error("aucun ventilateur listé: la mesure est impossible")
	}
}

func TestAssessVentilationFindings(t *testing.T) {
	in := input()
	in.LostLimit = 15 * time.Minute

	// Deux extracteurs arrêtés: critique, mais sans demande d'arrêt par les ventilateurs.
	in.Devs = []device{fan(1, 770), fan(2, 0), fan(3, 0), fan(15, 770), pr(13, 27), pr(14, 23)}
	fs := assess(in, defaultLimits())
	if got := keys(fs); !strings.Contains(got, "corsair-extraction:crit") || strings.Contains(got, "corsair-all") {
		t.Fatalf("deux extracteurs arrêtés doivent être critiques: %s", got)
	}
	for _, f := range fs {
		if f.Key == "corsair-extraction" && (!strings.Contains(f.Text, "dégradée") || strings.Contains(f.Text, "arrêt du NAS demandé")) {
			t.Fatalf("texte inattendu pour la ventilation dégradée: %s", f.Text)
		}
	}

	// Trois extracteurs arrêtés: perte totale, avec le délai annoncé.
	in.Devs = []device{fan(1, 0), fan(2, 0), fan(3, 0), fan(15, 770), pr(13, 27), pr(14, 23)}
	fs = assess(in, defaultLimits())
	found := false
	for _, f := range fs {
		if f.Key == "corsair-extraction" {
			found = true
			if f.Level != levelCrit || !strings.Contains(f.Text, "perdue") || !strings.Contains(f.Text, "15 min") {
				t.Fatalf("perte totale: texte ou gravité inattendus: %s %s", f.Level, f.Text)
			}
		}
	}
	if !found {
		t.Fatalf("perte totale: alerte attendue: %s", keys(fs))
	}

	// Un seul extracteur, ou l'admission seule: alerte simple.
	in.Devs = []device{fan(1, 770), fan(2, 0), fan(3, 770), fan(15, 770), pr(13, 27), pr(14, 23)}
	if got := keys(assess(in, defaultLimits())); got != "corsair-fan-2:warn" {
		t.Fatalf("un seul extracteur: alerte simple attendue: %s", got)
	}
	in.Devs = []device{fan(1, 770), fan(2, 770), fan(3, 770), fan(15, 0), pr(13, 27), pr(14, 23)}
	if got := keys(assess(in, defaultLimits())); got != "corsair-fan-15:warn" {
		t.Fatalf("admission seule: alerte simple attendue: %s", got)
	}

	// Deux extracteurs ABSENTS de la liste: même gravité que deux arrêtés.
	in.Devs = []device{fan(1, 770), fan(15, 770), pr(13, 27), pr(14, 23)}
	if got := keys(assess(in, defaultLimits())); !strings.Contains(got, "corsair-extraction:crit") {
		t.Fatalf("deux ventilateurs absents: %s", got)
	}

	// Veille de maintenance: aucune alerte de ventilateur.
	in.Maintenance = true
	in.Devs = []device{fan(1, 0), fan(2, 0), fan(3, 0), fan(15, 0), pr(13, 27), pr(14, 23)}
	if got := keys(assess(in, defaultLimits())); strings.Contains(got, "corsair") {
		t.Fatalf("la maintenance doit taire les alertes de ventilateurs: %s", got)
	}
}

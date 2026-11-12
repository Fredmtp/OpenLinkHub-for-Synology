package main

import (
	"strings"
	"testing"
	"time"
)

// Régression du 29/10 – 02/11/2026: la sonde du NAS est restée figée à 28,7 °C pendant 3 jours sans alerte.

func frozenKeys(fs []finding) []string {
	var out []string
	for _, f := range fs {
		if strings.HasSuffix(f.Key, "-frozen") {
			out = append(out, f.Key)
		}
	}
	return out
}

func frozenEval(s *shared, now time.Time, lim limits) []finding {
	in, _ := s.view(now)
	in.Enforce, in.NasCh, in.QnapCh = true, 13, 14
	return assess(in, lim)
}

func TestSondeFigeeAlerteApres4h(t *testing.T) {
	s := &shared{}
	lim := defaultLimits()
	s.setDevs([]device{pr(13, 28.7), pr(14, 25.7)}, true, at(0))
	h := 3600
	s.setDevs([]device{pr(13, 28.7), pr(14, 25.7)}, true, at(3*h))
	if got := frozenKeys(frozenEval(s, at(3*h), lim)); len(got) != 0 {
		t.Fatalf("pas d'alerte avant 4 h, reçu %v", got)
	}
	s.setDevs([]device{pr(13, 28.7), pr(14, 25.7)}, true, at(4*h+60))
	got := frozenKeys(frozenEval(s, at(4*h+60), lim))
	if len(got) != 2 || got[0] != "probe-NAS-frozen" || got[1] != "probe-QNAP-frozen" {
		t.Fatalf("les deux sondes figées doivent alerter, reçu %v", got)
	}
}

func TestSondeFigeeUneSeuleSonde(t *testing.T) {
	s := &shared{}
	lim := defaultLimits()
	h := 3600
	for i := 0; i <= 5*h; i += 600 {
		qnap := 25.0 + float64(i/600%3)*0.1 // la sonde du QNAP bouge
		s.setDevs([]device{pr(13, 28.7), pr(14, qnap)}, true, at(i))
	}
	got := frozenKeys(frozenEval(s, at(5*h), lim))
	if len(got) != 1 || got[0] != "probe-NAS-frozen" {
		t.Fatalf("seule la sonde du NAS est figée, reçu %v", got)
	}
}

func TestSondeFigeeLeChangementRemetLeCompteurAZero(t *testing.T) {
	s := &shared{}
	lim := defaultLimits()
	h := 3600
	s.setDevs([]device{pr(13, 28.7), pr(14, 25.7)}, true, at(0))
	s.setDevs([]device{pr(13, 28.8), pr(14, 25.7)}, true, at(3*h)) // la sonde NAS a bougé à 3 h
	s.setDevs([]device{pr(13, 28.8), pr(14, 25.8)}, true, at(3*h+10))
	if got := frozenKeys(frozenEval(s, at(6*h), lim)); len(got) != 0 {
		t.Fatalf("3 h depuis le dernier changement: pas d'alerte, reçu %v", got)
	}
	if got := frozenKeys(frozenEval(s, at(7*h+1200), lim)); len(got) != 2 {
		t.Fatalf("plus de 4 h depuis le dernier changement: 2 alertes, reçu %v", got)
	}
}

func TestSondeFigeeSondeIllisibleNeSignalePasFigee(t *testing.T) {
	s := &shared{}
	lim := defaultLimits()
	h := 3600
	s.setDevs([]device{pr(13, 28.7), pr(14, 25.7)}, true, at(0))
	s.setDevs([]device{pr(13, 0), pr(14, 25.7)}, true, at(1*h)) // illisible: couvert par « -missing »
	s.setDevs([]device{pr(13, 28.7), pr(14, 25.7)}, true, at(2*h))
	if got := frozenKeys(frozenEval(s, at(5*h), lim)); len(got) != 1 || got[0] != "probe-QNAP-frozen" {
		t.Fatalf("la sonde NAS repart de zéro après une lecture illisible, reçu %v", got)
	}
}

func TestSondeFigeeDesactivee(t *testing.T) {
	s := &shared{}
	lim := defaultLimits()
	lim.ProbeFrozen = 0
	s.setDevs([]device{pr(13, 28.7), pr(14, 25.7)}, true, at(0))
	if got := frozenKeys(frozenEval(s, at(48*3600), lim)); len(got) != 0 {
		t.Fatalf("contrôle désactivé: aucune alerte, reçu %v", got)
	}
}

func TestSondeFigeeApiHorsServiceNeSignaleRien(t *testing.T) {
	s := &shared{}
	lim := defaultLimits()
	s.setDevs([]device{pr(13, 28.7), pr(14, 25.7)}, true, at(0))
	s.setDevs(nil, false, at(5*3600)) // l'API ne répond plus: les valeurs sont périmées
	if got := frozenKeys(frozenEval(s, at(5*3600), lim)); len(got) != 0 {
		t.Fatalf("API injoignable: pas de verdict sur les sondes, reçu %v", got)
	}
}

func TestSondeFigeeCarteExplicative(t *testing.T) {
	c := describeFinding(finding{Key: "probe-NAS-frozen", Level: levelWarn, Text: "sonde du meuble NAS figée à 28.7 °C depuis 4h0m0s"}, assessInput{})
	if !strings.Contains(c.Title, "figée") || !strings.Contains(c.Action, "alimentation") {
		t.Errorf("carte inattendue: %q / %q", c.Title, c.Action)
	}
}

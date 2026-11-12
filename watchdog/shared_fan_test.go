package main

import (
	"testing"
	"time"
)

func fans(a, b, c, d int) []device {
	return []device{
		{ChannelID: 1, Description: "Fan", HasSpeed: true, RPM: a}, {ChannelID: 2, Description: "Fan", HasSpeed: true, RPM: b},
		{ChannelID: 3, Description: "Fan", HasSpeed: true, RPM: c}, {ChannelID: 15, Description: "Fan", HasSpeed: true, RPM: d},
	}
}

func TestFanStillSuitLesVitessesIdentiques(t *testing.T) {
	s := &shared{}
	s.setDevs(fans(1103, 1103, 1103, 1102), true, at(0))
	for m := 1; m <= 10; m++ {
		s.setDevs(fans(1103, 1103, 1103, 1102), true, at(m*60))
	}
	if got := s.fanStill(at(600)); got != 10*time.Minute {
		t.Fatalf("10 minutes de vitesses identiques: %v", got)
	}
	// Un seul ventilateur qui change suffit à remettre le décompte à zéro.
	s.setDevs(fans(1103, 1121, 1103, 1102), true, at(660))
	if got := s.fanStill(at(660)); got != 0 {
		t.Fatalf("un ventilateur a changé: décompte à zéro attendu: %v", got)
	}
	// Le ventilateur n°2 a bougé à 660: les autres sont inchangés depuis 0, le décompte est celui du plus récent changement.
	if got := s.fanStill(at(720)); got != 60*time.Second {
		t.Fatalf("décompte depuis le dernier changement: %v", got)
	}
}

func TestFanStillIgnoreArretsEtUnSeulVentilateur(t *testing.T) {
	s := &shared{}
	s.setDevs(fans(0, 0, 0, 0), true, at(0))
	if got := s.fanStill(at(3600)); got != 0 {
		t.Errorf("tous à l'arrêt: déjà couvert par la ventilation perdue, pas par cette règle: %v", got)
	}
	s = &shared{}
	s.setDevs(fans(900, 0, 0, 0), true, at(0))
	if got := s.fanStill(at(3600)); got != 0 {
		t.Errorf("un seul ventilateur en rotation: preuve insuffisante: %v", got)
	}
	s.setDevs(nil, true, at(3700)) // liste vide
	if got := s.fanStill(at(7200)); got != 0 {
		t.Errorf("liste vide: rien à suivre: %v", got)
	}
}

func TestHubStaleDansAssessEtVue(t *testing.T) {
	s := &shared{}
	for m := 0; m <= 11; m++ {
		s.setDevs(fans(1103, 1103, 1103, 1102), true, at(m*60))
	}
	in, _ := s.view(at(11 * 60))
	if in.FanStill != 11*time.Minute {
		t.Fatalf("la vue doit porter FanStill: %v", in.FanStill)
	}
	in.Enforce = true
	var got *finding
	fs := assess(in, defaultLimits())
	for i := range fs {
		if fs[i].Key == "hub-stale" {
			got = &fs[i]
		}
	}
	if got == nil || got.Level != levelWarn {
		t.Fatalf("alerte hub-stale attendue: %+v", fs)
	}
	if c := describeFinding(*got, in); c.Title == "" || c.Gravity != "haute" || c.Action == "" {
		t.Errorf("carte de l'alerte: %+v", c)
	}
	in.Maintenance = true
	for _, f := range assess(in, defaultLimits()) {
		if f.Key == "hub-stale" {
			t.Error("maintenance: pas d'alerte")
		}
	}
	in.Maintenance, in.FanStill = false, 9*time.Minute
	for _, f := range assess(in, defaultLimits()) {
		if f.Key == "hub-stale" {
			t.Error("9 minutes: pas encore d'alerte")
		}
	}
}

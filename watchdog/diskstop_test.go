package main

import (
	"testing"
	"time"
)

// ---- arrêt thermique des disques (limite voulue le 28/10/2026: 45 °C au plus) ----
// Arrêt à 45 °C maintenus 60 s, ou à 47 °C sur deux relevés. En dessous, aucun arrêt.

func diskSnap(temp float64, atSec int) nasSnap {
	s := healthyNAS()
	s.Disks[1].Temp = temp
	s.At = at(atSec)
	return s
}

func TestDiskDefaultsMatchTheUsersLimit(t *testing.T) {
	l := defaultLimits()
	if l.DiskWarn != 40 || l.DiskCrit != 43 || l.DiskShutdown != 45 || l.DiskInstant != 47 {
		t.Fatalf("seuils disque: %+v, attendu 40 / 43 / 45 / 47", l)
	}
	if *diskHold != time.Minute {
		t.Fatalf("maintien à 45 °C: %s, attendu 60 s", *diskHold)
	}
}

func TestDiskAt45ForOneMinuteShutsDown(t *testing.T) {
	m := newMon()
	if r := m.nasCheck(diskSnap(45, 1000), at(1000)); r != "" {
		t.Fatalf("arrêt immédiat à 45 °C: %s", r)
	}
	if r := m.nasCheck(diskSnap(45, 1030), at(1030)); r != "" {
		t.Fatalf("arrêt avant 60 s: %s", r)
	}
	if r := m.nasCheck(diskSnap(45, 1061), at(1061)); r == "" {
		t.Fatal("un disque à 45 °C pendant 60 s doit éteindre le NAS")
	}
}

func TestDiskAt47OnTwoReadingsShutsDown(t *testing.T) {
	m := newMon()
	if r := m.nasCheck(diskSnap(47, 1000), at(1000)); r != "" {
		t.Fatalf("un pic isolé ne doit pas éteindre: %s", r)
	}
	if r := m.nasCheck(diskSnap(47, 1060), at(1001)); r == "" {
		t.Fatal("deux relevés à 47 °C doivent éteindre le NAS")
	}
}

func TestDiskBelow45NeverShutsDown(t *testing.T) {
	m := newMon()
	for s := 1000; s < 5000; s += 60 {
		if r := m.nasCheck(diskSnap(44, s), at(s)); r != "" {
			t.Fatalf("44 °C est sous la limite: %s", r)
		}
	}
}

func TestDiskCooledBeforeOneMinuteDoesNotShutDown(t *testing.T) {
	m := newMon()
	m.nasCheck(diskSnap(46, 1000), at(1000))
	m.nasCheck(diskSnap(41, 1030), at(1030))
	if r := m.nasCheck(diskSnap(46, 1061), at(1061)); r != "" {
		t.Fatalf("le compteur aurait dû repartir de zéro: %s", r)
	}
}

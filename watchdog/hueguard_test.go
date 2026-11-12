package main

import (
	"strings"
	"testing"
	"time"
)

func hueBase(now time.Time) hueIn {
	return hueIn{
		Now: now, State: plugState{Known: true, On: true, Link: "connected", At: now}, Enforce: true, DevsOK: true,
		Devs: []device{
			{ChannelID: 1, Description: "Fan", HasSpeed: true, RPM: 900}, {ChannelID: 2, Description: "Fan", HasSpeed: true, RPM: 900},
			{ChannelID: 3, Description: "Fan", HasSpeed: true, RPM: 900}, {ChannelID: 15, Description: "Fan", HasSpeed: true, RPM: 900},
			{ChannelID: 13, IsTemperatureProbe: true, Temperature: 29}, {ChannelID: 14, IsTemperatureProbe: true, Temperature: 26},
		},
		Vent: ventOK, DiskMax: 33, DiskMaxOK: true, NasCh: 13, QnapCh: 14,
	}
}

func TestHuePriseEteinteRallumeeAussitot(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(1000))
	in.State.On = false
	d := g.decide(in)
	if d.Action != "on" || !strings.Contains(d.Why, "éteinte") {
		t.Fatalf("ordre ON immédiat attendu: %+v", d)
	}
	in.Now = at(1010) // prise réservée pendant l'exécution
	if d := g.decide(in); d.Action != "" {
		t.Fatalf("pas de second ordre pendant l'exécution: %+v", d)
	}
	in.Now = at(1070) // toujours éteinte une minute plus tard: nouvel ordre
	if d := g.decide(in); d.Action != "on" {
		t.Fatalf("nouvel ordre après une minute: %+v", d)
	}
}

func TestHuePriseEteinteReformuleArretApres5Min(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(1000))
	in.State.On = false
	g.decide(in)
	in.Now = at(1000 + 5*60)
	d := g.decide(in)
	if !strings.Contains(d.Shutdown, "éteinte depuis 5m0s") {
		t.Fatalf("arrêt attendu 5 minutes après la détection: %+v", d)
	}
}

func TestHuePriseEteinteLimiteDeTroisOrdresParHeure(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.State.On = false
	n := 0
	for s := 0; s < 280; s += 20 {
		in.Now = at(s)
		if g.decide(in).Action == "on" {
			n++
		}
	}
	if n != 3 {
		t.Errorf("3 ordres ON au plus par heure: %d", n)
	}
}

func TestHuePriseRallumeeRemetLeCompteurAZero(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.State.On = false
	g.decide(in)
	in.State.On = true
	in.Now = at(90)
	if d := g.decide(in); d.Action != "" || d.Shutdown != "" || !g.offSince.IsZero() {
		t.Fatalf("prise revenue: rien à faire et décompte remis à zéro: %+v", d)
	}
	in.State.On = false
	in.Now = at(2000)
	if d := g.decide(in); d.Action != "on" || d.Shutdown != "" {
		t.Fatalf("nouvelle coupure: nouvel ordre, pas d'arrêt: %+v", d)
	}
}

func TestHueHubAbsentCycleApres3Min(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.Devs = nil // liste vide: le Hub n'est plus vu
	if d := g.decide(in); d.Action != "" {
		t.Fatalf("pas de cycle au premier constat: %+v", d)
	}
	in.Now = at(170)
	if d := g.decide(in); d.Action != "" {
		t.Fatalf("pas de cycle avant 3 minutes: %+v", d)
	}
	in.Now = at(181)
	d := g.decide(in)
	if d.Action != "cycle" || !strings.Contains(d.Why, "Hub") {
		t.Fatalf("cycle attendu après 3 minutes: %+v", d)
	}
	in.Now = at(200) // exécution en cours
	if d := g.decide(in); d.Action != "" {
		t.Fatalf("pas de second cycle pendant l'exécution: %+v", d)
	}
}

func TestHueDeuxCyclesParJourAuPlus(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.Devs = nil
	cycles := 0
	for s := 0; s < 6*3600; s += 30 {
		in.Now = at(s)
		if g.decide(in).Action == "cycle" {
			cycles++
		}
	}
	if cycles != 2 {
		t.Errorf("2 cycles par 24 h au plus: %d", cycles)
	}
}

func TestHueNeCoupeJamaisLaPriseSiUnDisqueChauffe(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.Devs = nil
	in.DiskMax = 41
	g.decide(in)
	in.Now = at(600)
	if d := g.decide(in); d.Action != "" {
		t.Fatalf("disque à 41 °C: pas de coupure des ventilateurs: %+v", d)
	}
}

func TestHueDeuxSondesIllisiblesCycleApres10Min(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.Devs = []device{
		{ChannelID: 1, Description: "Fan", HasSpeed: true, RPM: 900},
		{ChannelID: 13, IsTemperatureProbe: true, Temperature: 0}, {ChannelID: 14, IsTemperatureProbe: true, Temperature: 0},
	}
	g.decide(in)
	in.Now = at(400)
	if d := g.decide(in); d.Action != "" {
		t.Fatalf("pas de cycle avant 10 minutes: %+v", d)
	}
	in.Now = at(601)
	if d := g.decide(in); d.Action != "cycle" || !strings.Contains(d.Why, "sondes") {
		t.Fatalf("cycle attendu après 10 minutes: %+v", d)
	}
}

func TestHueUneSeuleSondeIllisibleNeDeclenchePas(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.Devs[4].Temperature = 0 // la sonde NAS seulement
	g.decide(in)
	in.Now = at(3600)
	if d := g.decide(in); d.Action != "" {
		t.Fatalf("une seule sonde illisible: pas de cycle (alerte existante seulement): %+v", d)
	}
}

func TestHueSondeFigeeCycleImmediat(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.ProbeStill = map[int]time.Duration{13: 4*time.Hour + time.Minute}
	if d := g.decide(in); d.Action != "cycle" || !strings.Contains(d.Why, "même valeur") {
		t.Fatalf("sonde figée 4 h: cycle immédiat: %+v", d)
	}
}

func TestHueVentilationPerdueCycleEtArret5MinApresLOrdre(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.Vent = ventLost
	in.VentLostSince = at(0)
	for i := range in.Devs {
		if isFan(in.Devs[i]) {
			in.Devs[i].RPM = 0
		}
	}
	g.decide(in)
	in.Now = at(181)
	if d := g.decide(in); d.Action != "cycle" {
		t.Fatalf("cycle attendu après 3 minutes: %+v", d)
	}
	in.Now = at(181 + 4*60) // 4 minutes après l'ordre: pas encore
	if d := g.decide(in); d.Shutdown != "" {
		t.Fatalf("pas d'arrêt avant 5 minutes: %+v", d)
	}
	in.Now = at(181 + 5*60)
	if d := g.decide(in); !strings.Contains(d.Shutdown, "toujours perdue") {
		t.Fatalf("arrêt attendu 5 minutes après l'ordre: %+v", d)
	}
	// Ventilation revenue: plus d'arrêt.
	in.Vent, in.VentLostSince = ventOK, time.Time{}
	if d := g.decide(in); d.Shutdown != "" {
		t.Fatalf("ventilation rétablie: pas d'arrêt: %+v", d)
	}
}

func TestHueVieilOrdreNeCompteurPasPourUnNouveauProbleme(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	g.record("cycle", at(0))
	in := hueBase(at(10000))
	in.Vent, in.VentLostSince = ventLost, at(9990) // la perte est postérieure à l'ancien ordre
	if d := g.decide(in); d.Shutdown != "" {
		t.Fatalf("un ordre antérieur à la perte ne justifie pas l'arrêt: %+v", d)
	}
}

func TestHueGardeFousMaintenanceGraceEtEtatInconnu(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.State.On = false
	in.Maintenance = true
	if d := g.decide(in); d.Action != "" || d.Shutdown != "" {
		t.Fatalf("maintenance: aucune action: %+v", d)
	}
	in.Maintenance, in.Enforce = false, false
	if d := g.decide(in); d.Action != "" {
		t.Fatalf("délai de grâce: aucune action: %+v", d)
	}
	in.Enforce = true
	in.State = plugState{} // Bridge injoignable: état inconnu
	in.Devs = nil
	in.Now = at(7200)
	if d := g.decide(in); d.Action != "" || d.Shutdown != "" {
		t.Fatalf("état de la prise inconnu: les protections existantes seules s'appliquent: %+v", d)
	}
}

func TestHueOpenLinkHubInjoignableLaisseLeSuperviseur(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.DevsOK, in.Devs = false, nil
	g.decide(in)
	in.Now = at(3600)
	if d := g.decide(in); d.Action != "" {
		t.Fatalf("API injoignable: pas de cycle (c'est la relance d'OpenLinkHub): %+v", d)
	}
}

// Cas réel de la nuit du 12/11/2026: la prise est coupée à 0 h 15, le Hub n'a plus de courant mais OpenLinkHub
// continue d'afficher les dernières valeurs (1 103 tr/min, 30,6 °C): ni liste vide, ni ventilation « perdue ».
func TestHueHubGeleCycleImmediatApres10Min(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.FanStill = 9 * time.Minute
	if d := g.decide(in); d.Action != "" {
		t.Fatalf("9 minutes sans changement: pas encore de cycle (relevés sains: 3 minutes au plus): %+v", d)
	}
	in.Now, in.FanStill = at(60), 10*time.Minute
	d := g.decide(in)
	if d.Action != "cycle" || !strings.Contains(d.Why, "même vitesse") {
		t.Fatalf("10 minutes de vitesses identiques: cycle attendu: %+v", d)
	}
}

func TestHueHubGeleNeCoupePasUnNASQuiChauffe(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.FanStill, in.DiskMax = 20*time.Minute, 41
	if d := g.decide(in); d.Action != "" {
		t.Fatalf("disque à 41 °C: pas de coupure: %+v", d)
	}
}

func TestHueHubGeleToujoursGeleApresLeCycleArret5Min(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.FanStill = 10 * time.Minute
	if d := g.decide(in); d.Action != "cycle" {
		t.Fatalf("cycle attendu: %+v", d)
	}
	// Le Hub reste gelé: le décompte continue (le gel a commencé avant l'ordre).
	in.Now, in.FanStill = at(4*60), 14*time.Minute
	if d := g.decide(in); d.Shutdown != "" {
		t.Fatalf("pas d'arrêt avant 5 minutes: %+v", d)
	}
	in.Now, in.FanStill = at(5*60+1), 15*time.Minute+time.Second
	if d := g.decide(in); !strings.Contains(d.Shutdown, "sans signe de vie") {
		t.Fatalf("arrêt attendu 5 minutes après l'ordre: %+v", d)
	}
}

func TestHueHubRedemarreApresLeCycleNeProvoquePasDArret(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.FanStill = 10 * time.Minute
	g.decide(in)
	in.Now, in.FanStill = at(6*60), 2*time.Minute // les vitesses bougent de nouveau
	if d := g.decide(in); d.Shutdown != "" || d.Action != "" {
		t.Fatalf("Hub reparti: ni arrêt ni nouvel ordre: %+v", d)
	}
}

func TestHueSansDelaiUnSeulCyclePourLeMemeProbleme(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.ProbeStill = map[int]time.Duration{13: 5 * time.Hour} // cause sans délai: l'ordre est donné dans le même passage que le constat
	cycles := 0
	for s := 0; s < 50*60; s += 30 {
		in.Now = at(s)
		if g.decide(in).Action == "cycle" {
			cycles++
		}
	}
	if cycles != 1 {
		t.Errorf("un seul cycle dans les 50 premières minutes (le suivant n'est permis qu'après 1 h): %d", cycles)
	}
}

// Cas du 12/11/2026 à 9 h: essai de la prise (éteinte dans l'appli, rallumée par la surveillance). Le Hub a perdu
// son courant, OpenLinkHub garde des valeurs figées jusqu'à sa relance, mais les ventilateurs tournent: aucun arrêt.
func TestHueApresUnSimpleRallumageLesVitessesFigeesNeCausentPasDArret(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.State.On = false
	if d := g.decide(in); d.Action != "on" {
		t.Fatalf("ordre ON attendu: %+v", d)
	}
	in.State.On = true // le Bridge confirme: la prise est rallumée
	for _, c := range []struct {
		s     int
		still time.Duration
	}{{360, 6 * time.Minute}, {400, 7 * time.Minute}} {
		in.Now, in.FanStill = at(c.s), c.still // figées depuis le moment de la coupure, donc avant l'ordre
		if d := g.decide(in); d.Shutdown != "" {
			t.Fatalf("à %d s, %v de vitesses figées après un simple rallumage: aucun arrêt (ventilateurs alimentés): %+v", c.s, c.still, d)
		}
	}
}

// Si, sans relance d'OpenLinkHub, les vitesses restaient figées 10 minutes malgré une prise allumée, c'est le CYCLE
// (puis la relance) qui répare, pas l'arrêt: l'arrêt n'est permis que 5 minutes après un cycle resté sans effet.
func TestHueVitessesFigeesApresRallumageMeneAuCyclePuisSeulementALArret(t *testing.T) {
	g := newHueGuard(defaultHueCfg())
	in := hueBase(at(0))
	in.State.On = false
	g.decide(in)
	in.State.On = true
	in.Now, in.FanStill = at(660), 11*time.Minute
	d := g.decide(in)
	if d.Action != "cycle" || d.Shutdown != "" {
		t.Fatalf("10 minutes figées: un cycle, pas un arrêt: %+v", d)
	}
	in.Now, in.FanStill = at(660+4*60), 15*time.Minute
	if d := g.decide(in); d.Shutdown != "" {
		t.Fatalf("pas d'arrêt avant 5 minutes après le cycle: %+v", d)
	}
	in.Now, in.FanStill = at(660+5*60), 16*time.Minute
	if d := g.decide(in); d.Shutdown == "" {
		t.Fatalf("toujours figées 5 minutes après le cycle: arrêt attendu: %+v", d)
	}
}

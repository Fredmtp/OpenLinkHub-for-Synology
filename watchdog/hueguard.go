package main

import (
	"fmt"
	"time"
)

// Protection de l'alimentation des ventilateurs par la prise Hue (décisions du 12/11/2026).
//
// Contexte: la prise « Aération NAS » est coupée chaque nuit à 0 h 15 par une routine Hue (le NAS est alors
// éteint par le programme de DSM). Quand le NAS n'a pas été éteint, les ventilateurs perdent leur courant;
// OpenLinkHub, lui, CONTINUE D'AFFICHER les dernières valeurs du Hub (relevés du 12/11: 1 103 tr/min et 30,6 °C
// rigoureusement constants de 0 h 15 à 1 h 10 alors que les ventilateurs étaient arrêtés): la ventilation
// paraissait normale et seuls les 43 °C des disques ont arrêté le NAS (nuits du 11 et du 12/11). Lire les tr/min
// ne suffit donc pas: il faut l'état réel de la prise (Bridge) et détecter les valeurs figées.
//
// Règles (decide):
//  1. Prise ÉTEINTE (état lu sur le Bridge) alors que le NAS tourne → ordre ON tout de suite (une fois par minute
//     au plus, 3 par heure). Si elle reste éteinte 5 minutes → arrêt du NAS (les ventilateurs n'ont pas de courant).
//  2. Prise ALLUMÉE mais Hub bloqué (absent d'OpenLinkHub 3 min, ventilation perdue 3 min, TOUTES les vitesses de
//     ventilateurs identiques 10 min, deux sondes illisibles 10 min, ou sonde figée) → cycle: OFF 15 s puis ON,
//     comme la coupure manuelle du 02/11 qui avait réparé le
//     Commander DUO; ensuite relance d'OpenLinkHub. Au plus 2 cycles par 24 h, jamais si un disque est à 40 °C ou
//     plus (on ne coupe pas les ventilateurs d'un NAS qui chauffe).
//  3. Ventilation toujours perdue 5 minutes après un ordre à la prise, ou vitesses toujours figées 5 minutes après un
//     CYCLE complet → arrêt du NAS (au lieu de 15 minutes). Après tout ordre, OpenLinkHub est relancé (hueexec.go).
//
// Rien ne se fait en maintenance ni pendant le délai de grâce du démarrage; sans Bridge joignable (état de la
// prise inconnu), les protections existantes (ventilation perdue 15 min, températures) restent seules.

type hueCfg struct {
	HubDelay      time.Duration // Hub absent ou ventilation perdue avant cycle
	ProbeDelay    time.Duration // deux sondes illisibles avant cycle
	OrderGap      time.Duration // délai minimal entre deux ordres
	MaxOn         int           // ordres ON par heure
	MaxCycle      int           // cycles par 24 h
	ShutdownAfter time.Duration // arrêt du NAS si le problème persiste après l'ordre
	NoCutAt       float64       // température de disque à partir de laquelle on ne coupe jamais la prise
	FrozenAfter   time.Duration // durée de sonde figée qui déclenche un cycle (0 = pas de cycle pour cette cause)
	CycleRetry    time.Duration // délai avant un second cycle pour le même problème resté entier
	StaleAfter    time.Duration // vitesses de TOUS les ventilateurs rigoureusement identiques: Hub gelé (0 = pas de cycle pour cette cause)
}

func defaultHueCfg() hueCfg {
	return hueCfg{
		HubDelay: 3 * time.Minute, ProbeDelay: 10 * time.Minute, OrderGap: time.Minute,
		MaxOn: 3, MaxCycle: 2, ShutdownAfter: 5 * time.Minute, NoCutAt: 40, FrozenAfter: 4 * time.Hour, CycleRetry: time.Hour,
		StaleAfter: 10 * time.Minute,
	}
}

type hueIn struct {
	Now           time.Time
	State         plugState
	Enforce       bool
	Maintenance   bool
	DevsOK        bool
	Devs          []device
	Vent          ventState
	VentLostSince time.Time // début de la perte totale de ventilation (zéro si aucune)
	ProbeStill    map[int]time.Duration
	FanStill      time.Duration // depuis combien de temps aucun ventilateur n'a changé de vitesse (assessInput.FanStill)
	DiskMax       float64
	DiskMaxOK     bool
	NasCh, QnapCh int
}

type hueDecision struct {
	Action   string // "" (rien), "on" (rallumer), "cycle" (couper 15 s puis rallumer)
	Why      string
	Shutdown string // raison d'arrêt du NAS (vide = pas d'arrêt)
}

type hueGuard struct {
	cfg          hueCfg
	onOrders     []time.Time
	cycles       []time.Time
	lastOrder    time.Time
	lastKind     string
	busyUntil    time.Time
	offSince     time.Time
	troubleSince time.Time
	troubleKind  string
}

func newHueGuard(cfg hueCfg) *hueGuard { return &hueGuard{cfg: cfg} }

func within(ts []time.Time, now time.Time, d time.Duration) (kept []time.Time) {
	for _, t := range ts {
		if now.Sub(t) < d {
			kept = append(kept, t)
		}
	}
	return kept
}

func (g *hueGuard) resetWatch() {
	g.offSince, g.troubleSince, g.troubleKind = time.Time{}, time.Time{}, ""
}

// record note un ordre donné et réserve la prise le temps de son exécution.
func (g *hueGuard) record(kind string, now time.Time) {
	g.lastOrder, g.lastKind = now, kind
	if kind == "cycle" {
		g.cycles = append(g.cycles, now)
		g.busyUntil = now.Add(75 * time.Second)
	} else {
		g.onOrders = append(g.onOrders, now)
		g.busyUntil = now.Add(30 * time.Second)
	}
}

// Last donne le dernier ordre (pour la page d'état).
func (g *hueGuard) Last() (time.Time, string) { return g.lastOrder, g.lastKind }

// hubTrouble décrit un Hub bloqué malgré une prise allumée ("" si tout est lisible).
func hubTrouble(in hueIn, cfg hueCfg) (kind, text string, delay time.Duration) {
	if !in.DevsOK {
		return "", "", 0 // OpenLinkHub injoignable: c'est le travail du superviseur (relance d'OpenLinkHub)
	}
	listed := 0
	probeOK := map[int]bool{}
	for _, d := range in.Devs {
		switch {
		case isFan(d):
			listed++
		case d.IsTemperatureProbe:
			listed++
			if d.Temperature > 0 && d.Temperature <= 120 {
				probeOK[d.ChannelID] = true
			}
		}
	}
	switch {
	case listed == 0:
		return "hub", "le Hub n'apparaît plus dans OpenLinkHub (ni ventilateur ni sonde)", cfg.HubDelay
	case in.Vent == ventLost:
		return "ventilation", "OpenLinkHub voit la ventilation perdue (aucun ventilateur en rotation)", cfg.HubDelay
	case cfg.StaleAfter > 0 && in.FanStill >= cfg.StaleAfter:
		// Cas réel de la nuit du 12/11/2026: Hub sans courant, OpenLinkHub affiche les dernières valeurs (1 103 tr/min).
		return "gele", fmt.Sprintf("tous les ventilateurs affichent la même vitesse depuis %s (Hub gelé ou sans courant)", in.FanStill.Round(time.Minute)), 0
	}
	if cfg.FrozenAfter > 0 {
		for _, ch := range []int{in.NasCh, in.QnapCh} {
			if d, ok := in.ProbeStill[ch]; ok && d >= cfg.FrozenAfter {
				return "figee", fmt.Sprintf("la sonde du canal %d affiche la même valeur depuis %s", ch, d.Round(time.Minute)), 0
			}
		}
	}
	if !probeOK[in.NasCh] && !probeOK[in.QnapCh] {
		return "sondes", "les deux sondes du Commander DUO sont illisibles ou à 0 °C", cfg.ProbeDelay
	}
	return "", "", 0
}

// decide applique les règles. Il note lui-même l'ordre qu'il renvoie (record).
func (g *hueGuard) decide(in hueIn) hueDecision {
	now := in.Now
	g.onOrders = within(g.onOrders, now, time.Hour)
	g.cycles = within(g.cycles, now, 24*time.Hour)
	if !in.Enforce || in.Maintenance || !in.State.Known {
		g.resetWatch()
		return hueDecision{}
	}

	// 1. prise éteinte
	if !in.State.On {
		g.troubleSince, g.troubleKind = time.Time{}, ""
		if g.offSince.IsZero() {
			g.offSince = now
		}
		if off := now.Sub(g.offSince); off >= g.cfg.ShutdownAfter {
			return hueDecision{Shutdown: fmt.Sprintf("la prise des ventilateurs est éteinte depuis %s et ne se rallume pas", off.Round(time.Second))}
		}
		if now.Before(g.busyUntil) || now.Sub(g.lastOrder) < g.cfg.OrderGap || len(g.onOrders) >= g.cfg.MaxOn {
			return hueDecision{}
		}
		g.record("on", now)
		return hueDecision{Action: "on", Why: "la prise « Aération NAS » est éteinte alors que le NAS tourne: les ventilateurs du meuble n'ont plus de courant"}
	}
	g.offSince = time.Time{}

	// 3. ventilation toujours perdue 5 minutes après un ordre (qu'il ait porté sur la prise ou sur un cycle)
	if in.Vent == ventLost && !in.VentLostSince.IsZero() && g.lastOrder.After(in.VentLostSince) &&
		now.Sub(g.lastOrder) >= g.cfg.ShutdownAfter && now.Sub(in.VentLostSince) >= g.cfg.ShutdownAfter {
		return hueDecision{Shutdown: fmt.Sprintf("ventilation toujours perdue %s après l'ordre à la prise Hue", now.Sub(g.lastOrder).Round(time.Second))}
	}

	// 3 bis. Hub toujours gelé 5 minutes après un CYCLE complet (coupure, rallumage, relance d'OpenLinkHub): ni lecture ni
	// preuve que les ventilateurs tournent, on arrête. Jamais après un simple rallumage (règle 1): le Hub qui vient de
	// retrouver son courant n'est relu qu'après la relance d'OpenLinkHub, et ce matin (12/11, 9 h) l'arrêt déclenché là
	// était un faux positif alors que les ventilateurs tournaient. Après un cycle réussi, les vitesses changent et le
	// décompte repart à zéro.
	if g.cfg.StaleAfter > 0 && in.FanStill >= g.cfg.StaleAfter && g.lastKind == "cycle" && !g.lastOrder.IsZero() &&
		now.Add(-in.FanStill).Before(g.lastOrder) && now.Sub(g.lastOrder) >= g.cfg.ShutdownAfter {
		return hueDecision{Shutdown: fmt.Sprintf("ventilateurs toujours sans signe de vie (vitesses figées depuis %s) %s après l'ordre à la prise Hue",
			in.FanStill.Round(time.Minute), now.Sub(g.lastOrder).Round(time.Second))}
	}

	// 2. prise allumée mais Hub bloqué
	kind, text, delay := hubTrouble(in, g.cfg)
	if kind == "" {
		g.troubleSince, g.troubleKind = time.Time{}, ""
		return hueDecision{}
	}
	if g.troubleSince.IsZero() || kind != g.troubleKind {
		g.troubleSince, g.troubleKind = now, kind
	}
	wait := 2 * g.cfg.OrderGap
	if g.lastKind == "cycle" && !g.lastOrder.Before(g.troubleSince) {
		wait = g.cfg.CycleRetry // un cycle a déjà été fait pour ce problème: on attend son effet avant d'en refaire un
	}
	if now.Sub(g.troubleSince) < delay || now.Before(g.busyUntil) || now.Sub(g.lastOrder) < wait {
		return hueDecision{}
	}
	if in.DiskMaxOK && in.DiskMax >= g.cfg.NoCutAt {
		return hueDecision{} // le NAS chauffe: on ne coupe pas les ventilateurs, même pour réparer le Hub
	}
	if len(g.cycles) >= g.cfg.MaxCycle {
		return hueDecision{}
	}
	g.record("cycle", now)
	return hueDecision{Action: "cycle", Why: text}
}

package main

import (
	"fmt"
	"time"
)

// hueView est la vue de la prise Hue pour l'évaluation et l'affichage (voir hue.go et hueguard.go).
type hueView struct {
	Enabled   bool // un Bridge est configuré (HUE_BRIDGE, HUE_KEY et -hue-plug)
	State     plugState
	Now       time.Time
	LastOrder time.Time // dernier ordre donné à la prise par la surveillance
	LastKind  string    // "on" ou "cycle"
}

// hueFindings: prise éteinte alors que le NAS tourne, et Bridge injoignable depuis trop longtemps.
func hueFindings(v hueView, enforce, maintenance bool, lim limits) []finding {
	if !v.Enabled || !enforce || maintenance {
		return nil
	}
	var out []finding
	if v.State.Known && !v.State.On {
		out = append(out, finding{Key: "hue-off", Level: levelCrit,
			Text: "la prise « Aération NAS » est éteinte alors que le NAS tourne: les ventilateurs du meuble n'ont plus de courant"})
	}
	if !v.State.ErrSince.IsZero() && v.Now.Sub(v.State.ErrSince) >= lim.HueUnreachable {
		out = append(out, finding{Key: "hue-unreachable", Level: levelWarn,
			Text: fmt.Sprintf("le Bridge Hue ne répond plus depuis %s (%s): la prise des ventilateurs n'est plus surveillée ni commandable",
				v.Now.Sub(v.State.ErrSince).Round(time.Minute), v.State.LastErr)})
	}
	return out
}

// hueTile: tuile « Prise des ventilateurs » du tableau de bord d'OpenLinkHub.
func hueTile(v hueView) (tile, bool) {
	if !v.Enabled {
		return tile{}, false
	}
	t := tile{ID: "hue-plug", Title: "Prise des ventilateurs", Level: "ok"}
	switch {
	case !v.State.Known:
		t.Value, t.Level = "inconnue", "warn"
		t.Sub = "Bridge Hue pas encore lu"
	case v.State.On:
		t.Value = "allumée"
	default:
		t.Value, t.Level = "éteinte", "crit"
	}
	if v.State.Known {
		t.Sub = "liaison Zigbee: " + orDash(v.State.Link)
		if v.State.Link != "" && v.State.Link != "connected" {
			t.Sub += " (instable)"
		}
	}
	if !v.State.ErrSince.IsZero() && v.Now.Sub(v.State.ErrSince) >= time.Minute {
		t.Level = "warn"
		t.Sub2 = "Bridge injoignable depuis " + v.Now.Sub(v.State.ErrSince).Round(time.Minute).String()
	} else if !v.LastOrder.IsZero() {
		what := "rallumée"
		if v.LastKind == "cycle" {
			what = "coupée 15 s puis rallumée"
		}
		t.Sub2 = fmt.Sprintf("dernier ordre: %s à %s", what, v.LastOrder.Format("02/01 15:04"))
	}
	return t, true
}

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Protection contre la boucle d'arrêts (décision du 28/10/2026). Quand le NAS s'éteint parce que la
// ventilation est perdue, la surveillance écrit un marqueur (-autostop). Si le NAS est rallumé et que
// la cause persiste, il ne s'éteindrait pas à nouveau toutes les 15 min: pendant -autostop-guard
// (6 h par défaut) l'arrêt par la ventilation est suspendu, alors que les alertes, les bips et
// l'arrêt par la TEMPÉRATURE (disques à 45 °C, processeur, carte mère, sondes) restent actifs. Une
// notification explique le redémarrage. Seul l'arrêt pour perte de ventilation écrit ce marqueur.

type autoStop struct {
	At     time.Time `json:"at"`
	Reason string    `json:"raison"`
}

// writeAutoStop écrit le marqueur de façon atomique (fichier temporaire puis renommage).
func writeAutoStop(path string, at time.Time, reason string) error {
	if path == "" {
		return nil
	}
	b, err := json.Marshal(autoStop{At: at, Reason: reason})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readAutoStop lit le marqueur; absent ou illisible = pas d'arrêt automatique connu.
func readAutoStop(path string) (autoStop, bool) {
	if path == "" {
		return autoStop{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return autoStop{}, false
	}
	var s autoStop
	if json.Unmarshal(raw, &s) != nil || s.At.IsZero() {
		return autoStop{}, false
	}
	return s, true
}

// guardUntil renvoie la fin de la suspension de l'arrêt par la ventilation, et si elle est active.
func guardUntil(path string, window time.Duration, now time.Time) (autoStop, time.Time, bool) {
	s, ok := readAutoStop(path)
	if !ok || window <= 0 {
		return autoStop{}, time.Time{}, false
	}
	until := s.At.Add(window)
	if !until.After(now) {
		return s, until, false
	}
	return s, until, true
}

func autoStopMessage(s autoStop, until time.Time, dry bool, pageURL string) message {
	c := card{Key: "autostop-guard", Gravity: "haute", Where: "Surveillance", Equip: "DS1825+",
		Title: "NAS rallumé après un arrêt automatique",
		Summary: fmt.Sprintf("Le NAS s'était éteint tout seul le %s : %s. Si la ventilation est toujours perdue, il ne s'éteindra pas de nouveau avant %s (protection contre la boucle d'arrêts). Les bips et les alertes continuent, et l'arrêt par la température des disques (45 °C) reste actif.",
			s.At.Format("02/01 à 15:04"), s.Reason, until.Format("02/01 à 15:04")),
		Action: "Vérifiez la ventilation du meuble (alimentation et câble iCUE LINK du Hub, ventilateurs) avant de laisser le NAS en service."}
	return fixedMessage(c, "REDÉMARRAGE", dry, pageURL, 4, []string{"warning"},
		"NAS rallumé après un arrêt automatique ("+s.Reason+"): pas de nouvel arrêt par la ventilation avant "+until.Format("15:04")+".")
}

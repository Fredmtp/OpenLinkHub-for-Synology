package main

import (
	"flag"
	"os"
	"strconv"
	"strings"
	"time"
)

// Mise en veille de l'arrêt « ventilation » pendant une intervention (câbles, Hub, ventilateurs)
// NAS allumé. Le fichier -maintenance contient une date de fin en secondes Unix; maintenance.sh
// l'écrit. La veille ne dure jamais plus de maintenanceMax après l'écriture du fichier, et se
// termine d'elle-même: un oubli ne laisse pas la ventilation sans garde-fou.
//
// Pendant la veille, seule la règle « ventilateurs » (évaluate) cesse d'éteindre le NAS et les
// alertes de ventilateurs sont tues. Les seuils thermiques (sondes du meuble, processeur,
// disques, carte mère) restent actifs. Un fichier absent, illisible ou périmé = pas de veille.

const maintenanceMax = 4 * time.Hour

// maintRemind: tant que la maintenance dure, un rappel part à cet intervalle (0 = aucun rappel).
var maintRemind = flag.Duration("maintenance-remind", 30*time.Minute, "rappel par notification tant que la maintenance est active (0 = aucun)")

// maintenanceUntil renvoie la fin de la veille et si elle est active à l'instant now.
func maintenanceUntil(path string, now time.Time) (time.Time, bool) {
	if path == "" {
		return time.Time{}, false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || sec <= 0 {
		return time.Time{}, false
	}
	until := time.Unix(sec, 0)
	if limit := fi.ModTime().Add(maintenanceMax); until.After(limit) {
		until = limit
	}
	if !until.After(now) {
		return time.Time{}, false
	}
	return until, true
}

// maintenanceReminder rappelle que la maintenance est toujours active.
func maintenanceReminder(until time.Time, dry bool, pageURL string) message {
	c := card{Key: "maintenance", Gravity: "basse", Where: "Surveillance", Equip: "DS1825+",
		Title:   "Maintenance toujours en cours",
		Summary: "L'arrêt automatique lié aux ventilateurs est toujours suspendu, jusqu'à " + until.Format("15:04") + ". Les seuils de température restent actifs.",
		Action:  "Si l'intervention est terminée, mettez fin à la maintenance (page de commandes ou « sudo sh maintenance.sh off »)."}
	return fixedMessage(c, "MAINTENANCE", dry, pageURL, 3, []string{"hourglass"},
		"Maintenance toujours en cours (jusqu'à "+until.Format("15:04")+"): protection ventilation suspendue.")
}

func maintenanceMessage(on bool, until time.Time, dry bool, pageURL string) message {
	c := card{Key: "maintenance", Gravity: "basse", Where: "Surveillance", Equip: "DS1825+"}
	short := ""
	if on {
		c.Title = "Maintenance : arrêt ventilation en veille"
		c.Summary = "L'arrêt automatique lié aux ventilateurs est suspendu jusqu'à " + until.Format("15:04") +
			". Les seuils de température (sondes, processeur, disques, carte mère) restent actifs."
		c.Action = "Terminez l'intervention, puis lancez « sudo sh maintenance.sh off ». La veille s'arrête de toute façon à l'heure indiquée."
		short = "Maintenance: arrêt ventilation en veille jusqu'à " + until.Format("15:04") + "."
	} else {
		c.Title = "Maintenance terminée : arrêt ventilation rétabli"
		c.Summary = "L'arrêt automatique lié aux ventilateurs est de nouveau actif."
		c.Action = "Vérifiez sur la page d'état que les quatre ventilateurs tournent."
		short = "Maintenance terminée: arrêt ventilation rétabli."
	}
	return fixedMessage(c, "MAINTENANCE", dry, pageURL, 3, []string{"wrench"}, short)
}

package main

import (
	"flag"
	"os"
	"strconv"
	"time"
)

// Signe de vie de la surveillance (décision du 28/10/2026). Le programme écrit régulièrement la date
// dans le fichier -heartbeat; le superviseur (superviseur.sh, tâche DSM chaque minute) relance la
// surveillance et prévient si ce fichier n'est plus mis à jour depuis 2 minutes. Seule la date de
// modification du fichier compte pour le superviseur.
//
// Juste avant un arrêt demandé du NAS, un fichier « .stop » est créé à côté: le superviseur ne doit
// pas relancer la surveillance pendant que le NAS s'éteint.

var heartbeatFile = flag.String("heartbeat", "", "fichier de signe de vie, réécrit toutes les 20 s (lu par superviseur.sh); vide = pas de signe de vie")

const heartbeatEvery = 20 * time.Second

type beater struct{ last time.Time }

// tick écrit le signe de vie s'il y en a un à écrire (au plus un toutes les heartbeatEvery).
func (b *beater) tick(path string, now time.Time) error {
	if path == "" || (!b.last.IsZero() && now.Sub(b.last) < heartbeatEvery) {
		return nil
	}
	b.last = now
	return os.WriteFile(path, []byte(strconv.FormatInt(now.Unix(), 10)+"\n"), 0o644)
}

// markStopping signale au superviseur qu'un arrêt du NAS est en cours.
func markStopping(path string) {
	if path == "" {
		return
	}
	os.WriteFile(path+".stop", []byte(strconv.FormatInt(time.Now().Unix(), 10)+"\n"), 0o644)
}

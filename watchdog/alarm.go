package main

import (
	"log"
	"os"
	"strings"
	"syscall"
	"time"
)

// Alerte sonore par le buzzer du NAS (décision du 28/10/2026). Le buzzer ne dépend ni du Hub ni de
// l'alimentation des ventilateurs: c'est le signal qui marche même si tout le reste du meuble est
// hors tension. Sur le DS1825+, écrire « 2 » sur /dev/ttyS1 (en root) donne un bip court; ce
// comportement a été vérifié par l'utilisateur, on ne touche pas aux LED du NAS.
//
//   - alerte (série de bips en boucle): un disque, le processeur, la carte mère ou une sonde du meuble
//     au niveau critique, ou la ventilation dégradée ou perdue;
//   - avis (un bip isolé de temps en temps): ventilation partielle (un extracteur ou l'admission arrêté);
//   - silence temporaire: fichier écrit par silence.sh (l'alerte reste notifiée, seul le son est coupé).

type alarmLevel int

const (
	alarmNone alarmLevel = iota
	alarmNotice
	alarmAlert
)

// alarmLevelOf déduit le niveau sonore des anomalies en cours (voir assess): la veille de maintenance
// et le délai de grâce y sont déjà pris en compte.
func alarmLevelOf(fs []finding) alarmLevel {
	lvl := alarmNone
	for _, f := range fs {
		switch {
		case f.Level == levelCrit && (strings.HasPrefix(f.Key, "disk-temp-") || f.Key == "cpu-temp" || f.Key == "board-temp" ||
			strings.HasPrefix(f.Key, "probe-") || f.Key == "corsair-all" || f.Key == "corsair-extraction"):
			return alarmAlert
		case strings.HasPrefix(f.Key, "corsair-fan-"):
			lvl = alarmNotice
		}
	}
	return lvl
}

type alarmCfg struct {
	Gap    time.Duration // entre deux bips d'une série
	Count  int           // bips par série
	Pause  time.Duration // silence entre deux séries
	Notice time.Duration // entre deux bips isolés (0 = aucun)
}

// alarmTick joue ce qui est dû pour ce niveau et renvoie le délai avant le prochain examen. Les
// entrées-sorties (bip, attente) sont injectées pour pouvoir tester le rythme sans buzzer.
func alarmTick(level alarmLevel, silenced bool, now time.Time, lastNotice *time.Time, c alarmCfg, beep func(), sleep func(time.Duration)) time.Duration {
	if level != alarmNotice {
		*lastNotice = time.Time{}
	}
	if silenced {
		return time.Second
	}
	switch level {
	case alarmAlert:
		for i := 0; i < c.Count; i++ {
			beep()
			if i < c.Count-1 {
				sleep(c.Gap)
			}
		}
		return c.Pause
	case alarmNotice:
		if c.Notice > 0 && (lastNotice.IsZero() || now.Sub(*lastNotice) >= c.Notice) {
			beep()
			*lastNotice = now
		}
	}
	return time.Second
}

// ttyBeep envoie un bip court au buzzer. Une erreur n'est journalisée qu'une fois par changement.
func ttyBeep(path string, lastErr *string) func() {
	return func() {
		f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NOCTTY, 0)
		if err == nil {
			_, err = f.WriteString("2\n")
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		switch {
		case err != nil && *lastErr != err.Error():
			*lastErr = err.Error()
			log.Printf("ATTENTION: bip impossible sur %s: %v", path, err)
		case err == nil && *lastErr != "":
			*lastErr = ""
			log.Printf("Bip de nouveau possible sur %s", path)
		}
	}
}

// runAlarm tourne dans sa propre goroutine: un buzzer bloqué ne doit jamais retarder la sécurité.
func runAlarm(st *shared, c alarmCfg, beep func()) {
	var lastNotice time.Time
	for {
		wait := time.Second
		safely("alerte sonore", func() {
			now := time.Now()
			_, fs := st.view(now)
			_, silenced := maintenanceUntil(*silenceFile, now)
			wait = alarmTick(alarmLevelOf(fs), silenced, now, &lastNotice, c, beep, time.Sleep)
		})
		time.Sleep(wait)
	}
}

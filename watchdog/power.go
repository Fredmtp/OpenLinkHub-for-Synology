package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Détection des coupures de courant brutales: le compteur de retraits d'urgence des têtes (attribut
// SMART 192) augmente quand un disque perd son alimentation sans arrêt propre. Les compteurs sont
// gardés dans un fichier d'état, car une coupure arrête aussi la surveillance: au redémarrage, c'est
// la comparaison avec le fichier qui révèle la coupure.

type powerState struct {
	Retract map[string]int `json:"retract"`
	Saved   time.Time      `json:"saved"`
}

type powerEvent struct {
	Disk  string
	Delta int
}

// loadPowerState lit le fichier d'état; absent ou illisible, il renvoie une table vide
// (le premier relevé sert alors de référence, sans alerte).
func loadPowerState(path string) map[string]int {
	out := map[string]int{}
	if path == "" {
		return out
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var p powerState
	if json.Unmarshal(b, &p) != nil || p.Retract == nil {
		return out
	}
	return p.Retract
}

// savePowerState écrit le fichier de façon atomique (fichier temporaire puis renommage).
func savePowerState(path string, m map[string]int) error {
	if path == "" {
		return nil
	}
	b, err := json.MarshalIndent(powerState{Retract: m, Saved: time.Now()}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// retractEvents compare les compteurs actuels aux précédents. Une hausse est une coupure brutale.
// Une baisse (disque remplacé) ou un disque jamais vu ne déclenche rien: le repère est simplement mis à jour.
// Un disque absent du relevé (en veille par exemple) garde son ancien repère.
func retractEvents(prev map[string]int, disks []diskInfo) ([]powerEvent, map[string]int) {
	next := make(map[string]int, len(prev))
	for k, v := range prev {
		next[k] = v
	}
	var evs []powerEvent
	for _, d := range disks {
		if d.Standby || !d.RetractOK {
			continue
		}
		if p, known := prev[d.Name]; known && d.Retract > p {
			evs = append(evs, powerEvent{Disk: d.Name, Delta: d.Retract - p})
		}
		next[d.Name] = d.Retract
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].Disk < evs[j].Disk })
	return evs, next
}

func sameCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// isGeneralPowerLoss: une vraie coupure de courant touche (presque) tous les disques à la fois.
// Un seul disque, ou une minorité, évoque plutôt un choc, une vibration, une micro-coupure
// de son alimentation ou une réinitialisation de sa liaison SATA.
func isGeneralPowerLoss(affected, monitored int) bool {
	return monitored >= 2 && affected*4 >= monitored*3
}

// powerLossMessage joint au texte standard l'événement structuré: le répartiteur en fait une carte
// (haute pour une coupure générale, information pour un événement isolé).
func powerLossMessage(evs []powerEvent, monitored int, uptime time.Duration) message {
	m := powerLossText(evs, monitored, uptime)
	var parts []string
	for _, e := range evs {
		parts = append(parts, fmt.Sprintf("%s +%d", e.Disk, e.Delta))
	}
	key := "power-isolated"
	if isGeneralPowerLoss(len(evs), monitored) {
		key = "power-general"
	}
	text := fmt.Sprintf("retraits d'urgence en hausse sur %d disque(s) sur %d (%s), NAS en marche depuis %s",
		len(evs), monitored, strings.Join(parts, ", "), fmtDur(uptime))
	m.Items = []alertItem{{Kind: "event", F: finding{Key: key, Level: levelWarn, Text: text}}}
	return m
}

func powerLossText(evs []powerEvent, monitored int, uptime time.Duration) message {
	var parts []string
	for _, e := range evs {
		parts = append(parts, fmt.Sprintf("%s +%d", e.Disk, e.Delta))
	}
	detail := strings.Join(parts, ", ")
	if isGeneralPowerLoss(len(evs), monitored) {
		return message{
			Subject: "[NAS meuble] coupure de courant brutale détectée",
			Body: fmt.Sprintf("Le compteur de retraits d'urgence des têtes a augmenté sur %d disque(s) sur %d: %s.\n"+
				"Presque tous les disques sont touchés en même temps: cela correspond à une perte d'alimentation sans arrêt "+
				"propre (coupure de courant, onduleur défaillant, ou extinction forcée au bouton).\nLe NAS fonctionne depuis %s.\n"+
				"À vérifier: l'onduleur (autonomie, journal), le journal de DSM, et l'état de la ligne électrique.\n",
				len(evs), monitored, detail, fmtDur(uptime)),
			Short:    fmt.Sprintf("Coupure de courant brutale probable: %d disques sur %d touchés. Vérifiez l'onduleur.", len(evs), monitored),
			Priority: 4, Tags: []string{"zap"},
		}
	}
	return message{
		Subject: "[NAS meuble] événement isolé sur un disque (retrait d'urgence)",
		Body: fmt.Sprintf("Le compteur de retraits d'urgence des têtes a augmenté sur %d disque(s) sur %d: %s.\n"+
			"Ce n'est pas une coupure générale: les autres disques ne sont pas touchés et le NAS fonctionne depuis %s. "+
			"Causes possibles: choc ou vibration (porte du meuble, appareil voisin), micro-coupure de l'alimentation de ce "+
			"disque (contact de baie), ou réinitialisation de sa liaison SATA.\n"+
			"Sans autre signe (erreurs, hausse des compteurs de défauts), c'est une information, pas une panne. "+
			"Si cela se répète sur le même disque, vérifier la baie et ses contacts.\n",
			len(evs), monitored, detail, fmtDur(uptime)),
		Short:    fmt.Sprintf("Événement isolé sur %d disque sur %d (retrait d'urgence): pas de coupure générale.", len(evs), monitored),
		Priority: 3, Tags: []string{"information_source"},
	}
}

var (
	powerMu     sync.Mutex
	powerPrev   map[string]int
	powerLoaded bool
)

// checkPower est appelée à chaque relevé des capteurs du NAS.
func checkPower(n nasSnap) {
	powerMu.Lock()
	defer powerMu.Unlock()
	if !powerLoaded {
		powerPrev = loadPowerState(*stateFile)
		powerLoaded = true
	}
	evs, next := retractEvents(powerPrev, n.Disks)
	if len(evs) > 0 {
		monitored := 0
		for _, d := range n.Disks {
			if d.RetractOK && !d.Standby {
				monitored++
			}
		}
		logf("ATTENTION: retraits d'urgence en hausse sur %d disque(s) sur %d: %+v", len(evs), monitored, evs)
		m := powerLossMessage(evs, monitored, n.Uptime)
		if disp != nil {
			disp.post(m)
		} else if nt != nil {
			nt.post(m)
		}
	}
	if !sameCounts(powerPrev, next) {
		powerPrev = next
		if err := savePowerState(*stateFile, next); err != nil {
			logf("ATTENTION: fichier d'état non écrit (%v)", err)
		}
	}
}

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Option -preview DOSSIER: écrit trois mails d'exemple avec le vrai moteur (le même que pour les
// alertes réelles). Si une clé ANTHROPIC_API_KEY est disponible, l'IA rédige les textes: c'est aussi
// l'essai de bout en bout de l'analyse. Rien n'est envoyé, aucune notification n'est émise.

// sampleInput reproduit l'état du NAS au 26/10/2026 (valeurs réelles relevées ce jour-là).
func sampleInput() assessInput {
	disk := func(name, model string, temp float64, realloc, pending, reported int) diskInfo {
		return diskInfo{Name: name, Model: model, Temp: temp, TempOK: true, Health: "PASSED", PowerOn: 5207,
			Reallocated: realloc, Pending: pending, Uncorrect: pending, Reported: reported, Retract: 13, RetractOK: true}
	}
	pro, vn := "ST12000NE0008-2PK103", "ST12000VN0008-2JH101"
	return assessInput{
		HaveNAS: true, DevsOK: true, Enforce: true, NasCh: 13, QnapCh: 14,
		NAS: nasSnap{
			At: time.Now(), CPU: 44.9, CPUOK: true,
			BoardTemp: map[string]float64{"temp1": 30.8, "temp2": 35.0, "temp3": 35.8},
			NASFans:   map[int]int{1: 1338, 2: 1333},
			Disks: []diskInfo{disk("sata1", pro, 32, 0, 0, 0), disk("sata2", vn, 35, 69592, 40, 1087), disk("sata3", pro, 33, 0, 0, 0),
				disk("sata4", vn, 33, 38984, 0, 3564), disk("sata5", vn, 33, 0, 0, 0), disk("sata6", pro, 33, 0, 0, 0),
				disk("sata7", vn, 33, 0, 0, 0), disk("sata8", pro, 33, 0, 0, 0)},
			UPS:     upsInfo{Present: true, Status: "OL", Charge: 100, RuntimeS: 2003},
			RAID:    []raidInfo{{Name: "md2", Level: "raid6", Total: 8, Up: 8, Status: "ok"}},
			VolUsed: 22, VolOK: true, Uptime: 17*time.Hour + 11*time.Minute,
		},
		Devs: []device{
			{ChannelID: 1, Description: "Fan", HasSpeed: true, RPM: 931}, {ChannelID: 2, Description: "Fan", HasSpeed: true, RPM: 0},
			{ChannelID: 3, Description: "Fan", HasSpeed: true, RPM: 929}, {ChannelID: 15, Description: "Fan", HasSpeed: true, RPM: 937},
			{ChannelID: 13, IsTemperatureProbe: true, Temperature: 29.1}, {ChannelID: 14, IsTemperatureProbe: true, Temperature: 26.3},
		},
	}
}

func runPreview(dir string, an aiAnalyzer) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	in := sampleInput()
	hist := newHistory()
	healthy := in
	healthy.Devs = []device{{ChannelID: 1, Description: "Fan", HasSpeed: true, RPM: 931}, {ChannelID: 2, Description: "Fan", HasSpeed: true, RPM: 935},
		{ChannelID: 3, Description: "Fan", HasSpeed: true, RPM: 929}, {ChannelID: 15, Description: "Fan", HasSpeed: true, RPM: 937},
		{ChannelID: 13, IsTemperatureProbe: true, Temperature: 28.8}, {ChannelID: 14, IsTemperatureProbe: true, Temperature: 26.1}}
	hist.add(time.Now().Add(-6*time.Hour), metrics(healthy, 13, 14))
	healthy.Devs[4].Temperature = 26.4
	hist.add(time.Now().Add(-3*time.Hour), metrics(healthy, 13, 14))
	healthy.Devs[4].Temperature = 28.8
	hist.add(time.Now(), metrics(healthy, 13, 14))

	var files []string
	n := 0
	write := func(m message) {
		n++
		name := fmt.Sprintf("%d.html", n)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(m.HTML), 0o644); err == nil {
			files = append(files, fmt.Sprintf("%s (%s) -> %s", name, m.Subject, m.Short))
		}
	}
	d := newDispatcher(write, an, true, "http://192.168.1.10:27004/", map[string]string{
		"disk-errors-sata4":  "38 984 secteurs retirés, test étendu à faire",
		"disk-realloc-sata4": "38 984 secteurs retirés, test étendu à faire",
	}, func() (assessInput, []finding) { return in, nil }, hist, 8)
	d.timeoutNow, d.timeoutDig = 60*time.Second, 60*time.Second

	// 1. alerte critique: le test étendu du disque 2 a échoué
	d.process([]alertItem{{Kind: "fresh", F: finding{Key: "disk-bad-sata2", Level: levelCrit,
		Text: "disque sata2: 40 secteur(s) en attente, 40 non corrigeable(s); le test SMART étendu s'est arrêté sur une erreur de lecture au secteur 3786288 (10 % parcouru)"}}})
	// 2. alerte haute: un ventilateur du meuble arrêté
	d.process([]alertItem{{Kind: "fresh", F: finding{Key: "corsair-fan-2", Level: levelWarn, Text: "ventilateur du meuble canal 2 arrêté (0 tr/min)"}}})
	// 3. résumé: un événement isolé sur un disque, un volume qui se remplit
	d.process([]alertItem{
		{Kind: "event", F: finding{Key: "power-isolated", Level: levelWarn, Text: "retraits d'urgence en hausse sur 1 disque(s) sur 8 (sata1 +1), NAS en marche depuis 17 h 11 min"}},
		{Kind: "fresh", F: finding{Key: "volume", Level: levelWarn, Text: "volume 1 rempli à 91 %"}}})
	in.NAS.Disks[1].Standby = false
	d.state = func() (assessInput, []finding) {
		return healthy2(in), []finding{{Key: "disk-errors-sata4", Level: levelWarn, Text: "disque sata4: 3564 erreur(s) de lecture non corrigée(s) cumulée(s)"},
			{Key: "disk-realloc-sata4", Level: levelWarn, Text: "disque sata4: 38984 secteur(s) réalloué(s)"}}
	}
	d.digest(time.Date(2026, 10, 26, 8, 0, 0, 0, time.Local))
	for _, f := range files {
		fmt.Println(f)
	}
	if n == 0 {
		return fmt.Errorf("aucun mail produit")
	}
	return nil
}

// healthy2 remet les ventilateurs du meuble en état normal pour le résumé.
func healthy2(in assessInput) assessInput {
	out := in
	out.Devs = append([]device(nil), in.Devs...)
	for i := range out.Devs {
		if isFan(out.Devs[i]) && out.Devs[i].RPM == 0 {
			out.Devs[i].RPM = 935
		}
	}
	return out
}

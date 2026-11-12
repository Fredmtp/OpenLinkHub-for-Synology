package main

import (
	"fmt"
	"sort"
	"strings"
)

// tile est une tuile du tableau de bord d'OpenLinkHub (voir dist/static/js/nas-tiles.js).
type tile struct {
	ID    string `json:"id"` // lettres minuscules, chiffres et tirets: sert d'identifiant dans la page
	Title string `json:"title"`
	Value string `json:"value"`
	Sub   string `json:"sub"`
	Sub2  string `json:"sub2"`
	Level string `json:"level"` // ok, warn, crit
	Size  int    `json:"size"`  // largeur en colonnes Bootstrap (2 par défaut, 3 pour les disques)
}

// diskTile décrit un disque. Le niveau est le plus grave entre la température et l'état de surface.
func diskTile(d diskInfo, lim limits) tile {
	slot := strings.TrimPrefix(d.Name, "sata")
	t := tile{ID: "disk-" + d.Name, Title: "Disque " + slot, Sub: d.Model, Size: 3, Level: "ok"}
	if d.Standby {
		t.Value, t.Sub2 = "en veille", "température non lue"
		return t
	}
	if d.TempOK {
		t.Value = fmtT(d.Temp)
		t.Level = tempClass(d.Temp, lim.DiskWarn, lim.DiskCrit)
	} else {
		t.Value = "?"
	}
	var parts []string
	if d.Health != "" {
		parts = append(parts, "SMART "+d.Health)
	}
	if d.PowerOn > 0 {
		parts = append(parts, fmt.Sprintf("%d h", d.PowerOn))
	}
	switch {
	case d.Health != "" && d.Health != "PASSED", d.Pending > 0, d.Uncorrect > 0:
		t.Level = "crit"
	case (d.Reported > 0 || d.Reallocated > 0) && t.Level == "ok":
		t.Level = "warn"
	}
	if d.Reallocated > 0 {
		parts = append(parts, fmt.Sprintf("réalloués %d", d.Reallocated))
	}
	if d.Pending > 0 {
		parts = append(parts, fmt.Sprintf("en attente %d", d.Pending))
	}
	if d.Reported > 0 {
		parts = append(parts, fmt.Sprintf("erreurs %d", d.Reported))
	}
	t.Sub2 = strings.Join(parts, " · ")
	return t
}

// buildTiles compose les tuiles « NAS » à partir des mesures. Fonction pure.
func buildTiles(in assessInput, fs []finding, lim limits) []tile {
	var out []tile

	// Anomalies en cours
	lvl, sub := "ok", "aucune"
	if len(fs) > 0 {
		lvl = "warn"
		for _, f := range fs {
			if f.Level == levelCrit {
				lvl = "crit"
			}
		}
		sub = fs[0].Text
	}
	out = append(out, tile{ID: "anomalies", Title: "Anomalies NAS", Value: fmt.Sprintf("%d", len(fs)), Sub: sub, Level: lvl})

	if !in.HaveNAS {
		return out
	}
	s := in.NAS

	// Une tuile par disque: température, modèle, santé SMART et compteurs de défauts
	disks := append([]diskInfo(nil), s.Disks...)
	sort.Slice(disks, func(i, j int) bool { return disks[i].Name < disks[j].Name })
	for _, d := range disks {
		out = append(out, diskTile(d, lim))
	}

	// Carte mère
	if name, t, ok := s.maxBoardTemp(); ok {
		out = append(out, tile{ID: "board", Title: "Carte mère", Value: fmtT(t), Sub: name,
			Level: tempClass(t, lim.BoardWarn, lim.BoardShutdown)})
	}

	// Ventilateurs internes du NAS
	if s.NASFans != nil && len(lim.NASFans) > 0 {
		run, sum := 0, 0
		for _, n := range lim.NASFans {
			if r, ok := s.NASFans[n]; ok && r >= lim.MinRPM {
				run++
				sum += r
			}
		}
		l := "ok"
		if run < len(lim.NASFans) {
			l = "warn"
		}
		if run == 0 {
			l = "crit"
		}
		avg := 0
		if run > 0 {
			avg = sum / run
		}
		out = append(out, tile{ID: "nas-fans", Title: "Ventilateurs du NAS", Value: fmt.Sprintf("%d tr/min", avg),
			Sub: fmt.Sprintf("%d sur %d en marche", run, len(lim.NASFans)), Level: l})
	}

	// Onduleur
	if s.UPS.Present {
		l := "ok"
		switch {
		case strings.Contains(s.UPS.Status, "LB"):
			l = "crit"
		case strings.Contains(s.UPS.Status, "OB"):
			l = "warn"
		}
		out = append(out, tile{ID: "ups", Title: "Onduleur", Value: fmt.Sprintf("%.0f %%", s.UPS.Charge),
			Sub: fmt.Sprintf("%s · autonomie %.0f min", s.UPS.Status, s.UPS.RuntimeS/60), Level: l})
	}

	if ht, ok := hueTile(in.Hue); ok {
		out = append(out, ht)
	}

	// RAID de données: le plus grand ensemble
	var data *raidInfo
	for i := range s.RAID {
		if data == nil || s.RAID[i].Total > data.Total {
			data = &s.RAID[i]
		}
	}
	if data != nil {
		l, etat := "ok", "sain"
		switch {
		case data.remaining() <= 0:
			l, etat = "crit", "sans redondance"
		case data.Status == "degraded":
			l, etat = "crit", "dégradé"
		case data.Status == "resync":
			l, etat = "warn", "resynchronisation"
		}
		if data.Rebuilding && data.Progress > 0 {
			etat += fmt.Sprintf(" · %.0f %%", data.Progress)
		}
		if data.Scrubbing {
			etat += fmt.Sprintf(" · nettoyage des données %.0f %%", data.Progress)
		}
		out = append(out, tile{ID: "raid", Title: "RAID " + data.Name, Value: fmt.Sprintf("%d/%d", data.Up, data.Total),
			Sub: strings.ToUpper(data.Level) + " · " + etat, Level: l})
	}

	// Volume
	if s.VolOK {
		out = append(out, tile{ID: "volume", Title: "Volume 1", Value: fmt.Sprintf("%.0f %%", s.VolUsed), Sub: "espace utilisé",
			Level: tempClass(s.VolUsed, lim.VolWarn, lim.VolCrit)})
	}
	return out
}

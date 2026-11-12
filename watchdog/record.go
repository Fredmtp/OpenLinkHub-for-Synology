package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Enregistrement des températures: un fichier CSV par mois (temperatures-AAAA-MM.csv), une ligne par
// relevé du NAS (en pratique une par minute). Il sert à fixer les seuils d'après des mesures réelles
// (fenêtre fermée, été), à repérer une dérive lente (filtre encrassé, ventilateur qui faiblit) et à
// reconstituer un incident. Valeurs séparées par des virgules, point décimal, cellule vide = mesure
// absente. Une ligne d'en-tête est réécrite à chaque démarrage et chaque fois que les colonnes changent
// (disque remplacé ou ajouté). Rien n'est supprimé: environ 50 Mo par an.

type recCol struct{ Name, Val string }

type recorder struct {
	mu   sync.Mutex
	dir  string
	file *os.File
	path string
	cols string
}

func newRecorder(dir string) *recorder { return &recorder{dir: dir} }

func (r *recorder) write(now time.Time, row []recCol) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	path := filepath.Join(r.dir, "temperatures-"+now.Format("2006-01")+".csv")
	if r.file == nil || path != r.path {
		if r.file != nil {
			r.file.Close()
			r.file = nil
		}
		if err := os.MkdirAll(r.dir, 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		r.file, r.path, r.cols = f, path, ""
	}
	names := make([]string, 0, len(row)+1)
	vals := make([]string, 0, len(row)+1)
	names = append(names, "horodatage")
	vals = append(vals, now.Format("2006-01-02 15:04:05"))
	for _, c := range row {
		names = append(names, c.Name)
		vals = append(vals, c.Val)
	}
	header := strings.Join(names, ",")
	var b strings.Builder
	if header != r.cols {
		b.WriteString(header + "\n")
	}
	b.WriteString(strings.Join(vals, ",") + "\n")
	if _, err := r.file.WriteString(b.String()); err != nil {
		r.file.Close()
		r.file = nil
		return err
	}
	r.cols = header
	return nil
}

func temp1(v float64) string { return fmt.Sprintf("%.1f", v) }

// ventCode donne l'état de la ventilation sous forme sans accent, pour le CSV.
func ventCode(st ventState, ok bool) string {
	if !ok {
		return "inconnue"
	}
	switch st {
	case ventPartial:
		return "partielle"
	case ventDegraded:
		return "degradee"
	case ventLost:
		return "perdue"
	}
	return "ok"
}

// recordRow assemble les colonnes d'une ligne. Fonction pure. Les colonnes de ventilateurs sont
// fixes (extracteurs et admission attendus): un ventilateur ABSENT de la liste donne une cellule
// vide, un ventilateur listé à l'arrêt donne 0.
func recordRow(s nasSnap, haveNAS bool, devs []device, devsOK bool, lim limits, nasCh, qnapCh int, maintenance bool) []recCol {
	var row []recCol
	add := func(name, val string) { row = append(row, recCol{name, val}) }

	if haveNAS {
		disks := append([]diskInfo(nil), s.Disks...)
		sort.Slice(disks, func(i, j int) bool { return disks[i].Name < disks[j].Name })
		for _, d := range disks {
			v := ""
			if !d.Standby && d.TempOK {
				v = fmt.Sprintf("%.0f", d.Temp)
			}
			add("disque_"+d.Name, v)
		}
		v := ""
		if _, t, ok := s.maxDiskTemp(); ok {
			v = fmt.Sprintf("%.0f", t)
		}
		add("disque_max", v)
		v = ""
		if s.CPUOK {
			v = temp1(s.CPU)
		}
		add("processeur", v)
		v = ""
		if _, t, ok := s.maxBoardTemp(); ok {
			v = temp1(t)
		}
		add("carte_mere_max", v)
		for _, n := range lim.NASFans {
			v = ""
			if r, ok := s.NASFans[n]; ok {
				v = fmt.Sprint(r)
			}
			add(fmt.Sprintf("ventilo_nas_%d_tr_min", n), v)
		}
	}

	probe := func(ch int) string {
		if !devsOK {
			return ""
		}
		for _, d := range devs {
			if d.ChannelID == ch && d.IsTemperatureProbe && d.Temperature > 0 && d.Temperature <= 120 {
				return temp1(d.Temperature)
			}
		}
		return ""
	}
	add("sonde_nas", probe(nasCh))
	add("sonde_qnap", probe(qnapCh))

	chans := append([]int(nil), lim.ExtractionFans...)
	if lim.IntakeFan > 0 {
		chans = append(chans, lim.IntakeFan)
	}
	for _, ch := range chans {
		v := ""
		if devsOK {
			for _, d := range devs {
				if isFan(d) && d.ChannelID == ch {
					v = fmt.Sprint(d.RPM)
				}
			}
		}
		add(fmt.Sprintf("ventilo_ch%d_tr_min", ch), v)
	}

	st, _, ok := ventilationState(devs, lim)
	if !devsOK {
		ok = false
	}
	add("ventilation", ventCode(st, ok))
	if maintenance {
		add("maintenance", "1")
	} else {
		add("maintenance", "0")
	}
	// Avancement du nettoyage des données du plus grand RAID (0 = aucun nettoyage en cours): sert à écarter
	// de l'analyse des températures les périodes de relecture complète des disques.
	if haveNAS {
		prog := "0"
		var data *raidInfo
		for i := range s.RAID {
			if data == nil || s.RAID[i].Total > data.Total {
				data = &s.RAID[i]
			}
		}
		if data != nil && data.Scrubbing {
			prog = fmt.Sprintf("%.1f", data.Progress)
		}
		add("nettoyage_pct", prog)
	}
	return row
}

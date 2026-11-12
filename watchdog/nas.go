package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Mesures propres au NAS (hors Corsair), collectées lentement dans une goroutine à part.

type diskInfo struct {
	Model       string // modèle (sysfs)
	PowerOn     int    // heures de fonctionnement (attribut 9)
	Retract     int    // retraits d'urgence des têtes (attribut 192)
	RetractOK   bool   // l'attribut 192 a été lu
	Reported    int    // attribut 187
	Name        string // sata1…
	Temp        float64
	TempOK      bool
	Health      string // PASSED / FAILED / ""
	Reallocated int
	Pending     int
	Uncorrect   int
	Standby     bool
}

type upsInfo struct {
	Present  bool
	Status   string // OL, OB, LB… (upsc ups.status)
	Charge   float64
	RuntimeS float64
	LoadPct  float64
	InputV   float64
}

type raidInfo struct {
	Name       string
	Level      string
	Total      int
	Up         int
	Status     string  // "ok", "degraded", "resync"
	Rebuilding bool    // reconstruction ou resynchronisation en cours
	Progress   float64 // avancement en % (reconstruction ou nettoyage), si connu
	// Scrubbing: nettoyage des données (« check » du noyau): relecture de tous les disques et contrôle
	// de la parité. Ce n'est pas une panne: le statut reste « ok » et aucune alerte n'est levée.
	Scrubbing  bool
	Mismatch   int  // incohérences relevées par le dernier nettoyage (mismatch_cnt du noyau)
	MismatchOK bool // Mismatch a pu être lu
}

// margin est le nombre de disques que l'ensemble peut perdre sans perdre de données.
func (r raidInfo) margin() int {
	switch r.Level {
	case "raid6":
		return 2
	case "raid1":
		return r.Total - 1
	}
	return 1 // raid5, raid4, raid10 et inconnus: par prudence
}

// remaining est la marge de sécurité restante: 0 = plus aucune redondance.
func (r raidInfo) remaining() int { return r.margin() - (r.Total - r.Up) }

type nasSnap struct {
	At        time.Time
	CPU       float64
	CPUOK     bool
	BoardTemp map[string]float64 // adt7490: temp1…
	NASFans   map[int]int        // numéro -> tr/min (adt7490)
	Disks     []diskInfo
	UPS       upsInfo
	RAID      []raidInfo
	VolUsed   float64 // % utilisé du volume 1
	VolOK     bool
	Uptime    time.Duration
	Qnap      qnapSnap // baie QNAP TR-004 (présence, erreurs USB du noyau)
}

func readInt(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return v, err == nil
}

// hwmonByName renvoie le dossier hwmon dont le nom correspond.
func hwmonByName(name string) string {
	names, _ := filepath.Glob("/sys/class/hwmon/hwmon*/name")
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err == nil && strings.TrimSpace(string(b)) == name {
			return filepath.Dir(n)
		}
	}
	return ""
}

// readBoard lit les sondes et ventilateurs du contrôleur de la carte mère (adt7490).
func readBoard() (map[string]float64, map[int]int) {
	dir := hwmonByName("adt7490")
	if dir == "" {
		return nil, nil
	}
	temps := map[string]float64{}
	fans := map[int]int{}
	for i := 1; i <= 6; i++ {
		if v, ok := readInt(filepath.Join(dir, fmt.Sprintf("temp%d_input", i))); ok {
			temps[fmt.Sprintf("temp%d", i)] = float64(v) / 1000
		}
		if v, ok := readInt(filepath.Join(dir, fmt.Sprintf("fan%d_input", i))); ok {
			fans[i] = v
		}
	}
	return temps, fans
}

// run exécute une commande avec un délai maximal, pour qu'un disque ou un outil bloqué
// ne puisse jamais figer la collecte.
func run(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// parseSmart extrait de la sortie de `smartctl -H -A` la température et les compteurs de défauts.
func parseSmart(out string) diskInfo {
	var d diskInfo
	if strings.Contains(out, "STANDBY") || strings.Contains(out, "SLEEP") {
		d.Standby = true
		return d
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "overall-health self-assessment test result:") {
			d.Health = strings.TrimSpace(line[strings.LastIndex(line, ":")+1:])
			continue
		}
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		raw, err := strconv.Atoi(f[9])
		if err != nil {
			continue
		}
		switch f[0] {
		case "194":
			d.Temp, d.TempOK = float64(raw), true
		case "190":
			if !d.TempOK { // « Airflow_Temperature » sert de repli
				d.Temp, d.TempOK = float64(raw), true
			}
		case "9":
			d.PowerOn = raw
		case "192":
			d.Retract, d.RetractOK = raw, true // retraits d'urgence des têtes: coupures de courant brutales
		case "5":
			d.Reallocated = raw // « Reallocated_Sector_Ct » ou « Retired_Sectors_Count » (Seagate)
		case "187":
			d.Reported = raw // erreurs de lecture non corrigées remontées à l'hôte
		case "197":
			d.Pending = raw
		case "198":
			d.Uncorrect = raw
		}
	}
	return d
}

// diskModel lit le modèle d'un disque dans sysfs (ex. « ST12000VN0008-2JH101 »).
func diskModel(name string) string {
	b, err := os.ReadFile("/sys/block/" + name + "/device/model")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// parseSynodisk lit la sortie de `synodisk --enum -t internal` et donne, pour chaque chemin noyau
// (/dev/sata1…), le numéro de baie de DSM (« Disk id »). Les noms /dev/sataN sont attribués dans
// l'ordre de détection au démarrage et peuvent être croisés d'un démarrage à l'autre; le numéro de
// baie, lui, suit le port physique.
func parseSynodisk(out string) map[string]int {
	m := map[string]int{}
	id := 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), ">>"))
		switch {
		case strings.HasPrefix(line, "Disk id:"):
			id, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Disk id:")))
		case strings.HasPrefix(line, "Disk path:") && id > 0:
			m[strings.TrimSpace(strings.TrimPrefix(line, "Disk path:"))] = id
			id = 0
		}
	}
	return m
}

// bayNames donne le nom de baie (« sata2 » = disque 2 de DSM) de chaque périphérique. La table n'est
// utilisée que si elle couvre tous les périphériques avec des numéros distincts; sinon chaque disque
// garde son nom noyau (mieux vaut un nom possiblement croisé que deux disques sous le même nom).
func bayNames(devs []string, bays map[string]int) map[string]string {
	names := map[string]string{}
	seen := map[int]bool{}
	for _, dev := range devs {
		id, ok := bays[dev]
		if !ok || seen[id] {
			return nil
		}
		seen[id] = true
		names[dev] = "sata" + strconv.Itoa(id)
	}
	return names
}

func readDisks() []diskInfo {
	devs, _ := filepath.Glob("/dev/sata?")
	sort.Strings(devs)
	synoOut, _ := run(8*time.Second, "/usr/syno/bin/synodisk", "--enum", "-t", "internal")
	names := bayNames(devs, parseSynodisk(synoOut))
	var out []diskInfo
	for _, dev := range devs {
		// -n standby: ne réveille jamais un disque en veille
		txt, _ := run(15*time.Second, "smartctl", "-n", "standby", "-d", "sat", "-H", "-A", dev)
		d := parseSmart(txt)
		d.Name = filepath.Base(dev)
		d.Model = diskModel(d.Name)
		if bay, ok := names[dev]; ok {
			d.Name = bay
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// parseUPS lit la sortie de `upsc ups@localhost` (lignes « clé: valeur »).
func parseUPS(out string) upsInfo {
	kv := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, ": "); i > 0 {
			kv[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+2:])
		}
	}
	if kv["ups.status"] == "" {
		return upsInfo{}
	}
	num := func(k string) float64 { v, _ := strconv.ParseFloat(kv[k], 64); return v }
	return upsInfo{Present: true, Status: kv["ups.status"], Charge: num("battery.charge"),
		RuntimeS: num("battery.runtime"), LoadPct: num("ups.load"), InputV: num("input.voltage")}
}

func readUPS() upsInfo {
	out, err := run(8*time.Second, "upsc", "ups@localhost")
	if err != nil && !strings.Contains(out, "ups.status") {
		return upsInfo{}
	}
	return parseUPS(out)
}

var reMdLine = regexp.MustCompile(`^(md\d+) : \S+ (\S+)`)
var reMdState = regexp.MustCompile(`\[(\d+)/(\d+)\] \[([U_]+)\]`)
var reMdProgress = regexp.MustCompile(`(resync|recovery|reshape|check)\s*=\s*([0-9.]+)%`)

// parseMdstat décrit l'état de chaque ensemble RAID (/proc/mdstat).
func parseMdstat(txt string) []raidInfo {
	var out []raidInfo
	var cur *raidInfo
	sc := bufio.NewScanner(strings.NewReader(txt))
	for sc.Scan() {
		line := sc.Text()
		if m := reMdLine.FindStringSubmatch(line); m != nil {
			out = append(out, raidInfo{Name: m[1], Level: m[2], Status: "ok"})
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if m := reMdState.FindStringSubmatch(line); m != nil {
			cur.Total, _ = strconv.Atoi(m[1])
			cur.Up, _ = strconv.Atoi(m[2])
			if cur.Up < cur.Total || strings.Contains(m[3], "_") {
				cur.Status = "degraded"
			}
		}
		if m := reMdProgress.FindStringSubmatch(line); m != nil && m[1] == "check" {
			// Nettoyage des données: le RAID reste « ok ».
			cur.Scrubbing = true
			cur.Progress, _ = strconv.ParseFloat(m[2], 64)
		} else if m != nil {
			cur.Rebuilding = true
			cur.Progress, _ = strconv.ParseFloat(m[2], 64)
			if cur.Status == "ok" {
				cur.Status = "resync"
			}
		} else if strings.Contains(line, "resync") || strings.Contains(line, "recovery") || strings.Contains(line, "reshape") {
			cur.Rebuilding = true
			if cur.Status == "ok" {
				cur.Status = "resync"
			}
		}
	}
	return out
}

func readVolume(path string) (float64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil || st.Blocks == 0 {
		return 0, false
	}
	return 100 * float64(st.Blocks-st.Bfree) / float64(st.Blocks), true
}

func readUptime() time.Duration {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	s, _ := strconv.ParseFloat(f[0], 64)
	return time.Duration(s * float64(time.Second))
}

// collectNAS réunit toutes les mesures du NAS. Appelée toutes les minutes hors de la boucle de sécurité.
func collectNAS() nasSnap {
	s := nasSnap{At: time.Now()}
	s.CPU, s.CPUOK = cpuTemp()
	s.BoardTemp, s.NASFans = readBoard()
	s.Disks = readDisks()
	s.UPS = readUPS()
	if b, err := os.ReadFile("/proc/mdstat"); err == nil {
		s.RAID = parseMdstat(string(b))
		for i := range s.RAID {
			if raw, err := os.ReadFile("/sys/block/" + s.RAID[i].Name + "/md/mismatch_cnt"); err == nil {
				if n, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
					s.RAID[i].Mismatch, s.RAID[i].MismatchOK = n, true
				}
			}
		}
	}
	s.VolUsed, s.VolOK = readVolume("/volume1")
	s.Uptime = readUptime()
	s.Qnap = readQnap(*qnapVendor)
	return s
}

// maxDiskTemp renvoie le disque le plus chaud mesuré.
func (s nasSnap) maxDiskTemp() (string, float64, bool) {
	name, max, ok := "", 0.0, false
	for _, d := range s.Disks {
		if d.TempOK && (!ok || d.Temp > max) {
			name, max, ok = d.Name, d.Temp, true
		}
	}
	return name, max, ok
}

// maxBoardTemp renvoie la sonde de carte la plus chaude.
func (s nasSnap) maxBoardTemp() (string, float64, bool) {
	name, max, ok := "", 0.0, false
	for k, v := range s.BoardTemp {
		if !ok || v > max {
			name, max, ok = k, v, true
		}
	}
	return name, max, ok
}

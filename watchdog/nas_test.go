package main

import (
	"strings"
	"testing"
	"time"
)

// Sorties réelles relevées sur le DS1825+ le 2026-10-25.
const smartSata2 = `smartctl 7.4 2023-08-01 r5530 [x86_64-linux-5.10.55+] (local build)

=== START OF READ SMART DATA SECTION ===
SMART overall-health self-assessment test result: PASSED
SMART Attributes Data Structure revision number: 10
ID# ATTRIBUTE_NAME          FLAG     VALUE WORST THRESH TYPE      UPDATED  WHEN_FAILED RAW_VALUE
  5 Reallocated_Sector_Ct   0x0033   100   100   010    Pre-fail  Always       -       0
  9 Power_On_Hours                                                   0x0032   095   095   000    Old_age   Always       -       5191
190 Airflow_Temperature                                              0x0022   062   046   040    Old_age   Always       -       38 (Min/Max 25/39)
194 Temperature                                                      0x0022   038   054   000    Old_age   Always       -       38 (0 18 0 0 0)
197 Current_Pending_Sector  0x0012   100   100   000    Old_age   Always       -       0
198 Offline_Uncorrectable   0x0010   100   100   000    Old_age   Offline      -       0
`

func TestParseSmart(t *testing.T) {
	d := parseSmart(smartSata2)
	if !d.TempOK || d.Temp != 38 {
		t.Fatalf("température: %+v", d)
	}
	if d.Health != "PASSED" || d.Reallocated != 0 || d.Pending != 0 {
		t.Fatalf("santé: %+v", d)
	}
}

// Sortie réelle de /dev/sata2 (Seagate IronWolf) relevée le 2026-10-25: les attributs 5 et 187
// portent des noms propres à Seagate, la lecture se fait par numéro.
const smartSata2Defects = `SMART overall-health self-assessment test result: PASSED
ID# ATTRIBUTE_NAME                                                   FLAG     VALUE WORST THRESH TYPE      UPDATED  WHEN_FAILED RAW_VALUE
  1 Raw_Error_Rate                                                   0x000f   083   049   044    Pre-fail  Always       -       217968962
  5 Retired_Sectors_Count                                            0x0033   083   083   010    Pre-fail  Always       -       69592
187 Reported_Uncorrectables                                          0x0032   001   001   000    Old_age   Always       -       1087
190 Airflow_Temperature                                              0x0022   062   046   040    Old_age   Always       -       38 (Min/Max 25/39)
194 Temperature                                                      0x0022   038   054   000    Old_age   Always       -       38 (0 18 0 0 0)
195 ECC_On_the_Fly_Count                                             0x001a   004   001   000    Old_age   Always       -       217968962
197 Pending-Sparing_Count                                            0x0012   100   099   000    Old_age   Always       -       40
198 Uncorrectable_Sectors_Count                                      0x0010   100   099   000    Old_age   Offline      -       40
`

func TestParseSmartSeagateDefects(t *testing.T) {
	d := parseSmart(smartSata2Defects)
	if d.Temp != 38 || d.Reallocated != 69592 || d.Reported != 1087 || d.Pending != 40 || d.Uncorrect != 40 {
		t.Fatalf("%+v", d)
	}
}

func TestAssessDiskGrowthFromBaseline(t *testing.T) {
	in := input()
	in.NAS.Disks[1].Reported, in.NAS.Disks[1].Reallocated = 1087, 69592
	in.Baseline = map[string]diskBase{"sata2": {Reported: 1087, Reallocated: 69592, At: time.Unix(0, 0)}}
	got := keys(assess(in, defaultLimits()))
	if strings.Contains(got, "disk-growth") {
		t.Fatalf("un défaut stable n'est pas une aggravation: %s", got)
	}
	if !strings.Contains(got, "disk-errors-sata2:warn") {
		t.Fatalf("l'erreur cumulée doit rester signalée: %s", got)
	}
	in.NAS.Disks[1].Reported = 1090
	if got = keys(assess(in, defaultLimits())); !strings.Contains(got, "disk-growth-sata2:crit") {
		t.Fatalf("+3 erreurs doivent être signalées comme aggravation: %s", got)
	}
}

func TestBaselineFollowsReplacedDisk(t *testing.T) {
	st := &shared{}
	old := healthyNAS()
	old.Disks[1].Reported, old.Disks[1].Reallocated, old.Disks[1].Pending = 1087, 69592, 40
	st.setNAS(old)
	// sata2 est remplacé: compteurs à zéro. Le repère doit suivre vers le bas.
	fresh := healthyNAS()
	fresh.At = time.Unix(2000, 0)
	st.setNAS(fresh)
	if b := st.baseline["sata2"]; b.Reported != 0 || b.Reallocated != 0 || b.Pending != 0 {
		t.Fatalf("repère non réinitialisé: %+v", b)
	}
	// Le nouveau disque se dégrade ensuite: l'aggravation doit être vue.
	bad := healthyNAS()
	bad.At = time.Unix(3000, 0)
	bad.Disks[1].Reported = 2
	in := input()
	in.NAS, in.Baseline = bad, st.baseline
	if got := keys(assess(in, defaultLimits())); !strings.Contains(got, "disk-growth-sata2:crit") {
		t.Fatalf("aggravation du nouveau disque non détectée: %s", got)
	}
}

func raidInput(r raidInfo) assessInput {
	in := input()
	in.NAS.RAID = []raidInfo{r}
	return in
}

func TestRaidMargins(t *testing.T) {
	cases := []struct {
		name string
		r    raidInfo
		want string
	}{
		{"raid6 un disque manquant: marge restante 1", raidInfo{Name: "md2", Level: "raid6", Total: 8, Up: 7, Status: "degraded"}, "raid-md2:crit"},
		{"raid6 deux manquants: plus de redondance", raidInfo{Name: "md2", Level: "raid6", Total: 8, Up: 6, Status: "degraded"}, "raid-md2:crit,raid-md2-severe:crit"},
		{"raid1 à 8 copies, un manquant: simple alerte", raidInfo{Name: "md0", Level: "raid1", Total: 8, Up: 7, Status: "degraded"}, "raid-md0:warn"},
		{"raid5 un manquant: plus de redondance", raidInfo{Name: "md3", Level: "raid5", Total: 4, Up: 3, Status: "degraded"}, "raid-md3:crit,raid-md3-severe:crit"},
		{"sain", raidInfo{Name: "md2", Level: "raid6", Total: 8, Up: 8, Status: "ok"}, ""},
	}
	for _, c := range cases {
		if got := keys(assess(raidInput(c.r), defaultLimits())); got != c.want {
			t.Errorf("%s: obtenu %q, attendu %q", c.name, got, c.want)
		}
	}
}

func TestParseMdstatRebuildProgress(t *testing.T) {
	txt := `md2 : active raid6 sata7p5[0] sata1p5[4] sata6p5[5] sata3p5[6] sata8p5[7] sata4p5[3] sata5p5[2] sata2p5[8]
      70248929280 blocks super 1.2 level 6, 64k chunk, algorithm 2 [8/7] [UUUUUU_U]
      [==>..................]  recovery = 12.3% (1441792/11718819840) finish=2740.0min speed=71000K/sec
`
	r := parseMdstat(txt)
	if len(r) != 1 || !r[0].Rebuilding || r[0].Progress != 12.3 || r[0].Status != "degraded" || r[0].Up != 7 {
		t.Fatalf("%+v", r)
	}
	f := assess(raidInput(r[0]), defaultLimits())
	if len(f) != 1 || !strings.Contains(f[0].Text, "reconstruction à 12 %") {
		t.Fatalf("l'avancement doit apparaître dans l'alerte: %+v", f)
	}
}

func TestRaidRemindersEverySixHours(t *testing.T) {
	c := &capture{}
	a := newAlerter(c.post)
	deg := []finding{{Key: "raid-md2", Level: levelCrit, Text: "RAID md2 dégradé"}}
	a.reconcile(at(0), deg)
	a.reconcile(at(70), deg)
	if len(c.msgs) != 1 {
		t.Fatalf("première alerte attendue: %d", len(c.msgs))
	}
	a.reconcile(at(70+3601), deg) // une heure plus tard: pas de rappel pour un RAID dégradé
	if len(c.msgs) != 1 {
		t.Fatalf("pas de rappel horaire pour un RAID dégradé: %d messages", len(c.msgs))
	}
	a.reconcile(at(70+6*3600+1), deg)
	if len(c.msgs) != 2 {
		t.Fatalf("rappel attendu à 6 h: %d messages", len(c.msgs))
	}

	c2 := &capture{}
	b := newAlerter(c2.post)
	sev := []finding{{Key: "raid-md2-severe", Level: levelCrit, Text: "RAID md2 SANS REDONDANCE"}}
	b.reconcile(at(0), sev)
	b.reconcile(at(70), sev)
	b.reconcile(at(70+3601), sev)
	if len(c2.msgs) != 2 {
		t.Fatalf("l'absence de redondance garde le rappel horaire: %d messages", len(c2.msgs))
	}
}

func tileByID(ts []tile, id string) (tile, bool) {
	for _, t := range ts {
		if t.ID == id {
			return t, true
		}
	}
	return tile{}, false
}

func TestTilesHealthy(t *testing.T) {
	ts := buildTiles(input(), nil, defaultLimits())
	for id, want := range map[string]string{
		"anomalies": "ok", "disk-sata1": "ok", "disk-sata2": "ok", "board": "ok",
		"nas-fans": "ok", "ups": "ok", "raid": "ok", "volume": "ok",
	} {
		tl, ok := tileByID(ts, id)
		if !ok {
			t.Fatalf("tuile %s absente", id)
		}
		if tl.Level != want {
			t.Errorf("tuile %s: niveau %s, attendu %s (%+v)", id, tl.Level, want, tl)
		}
	}
	if tl, _ := tileByID(ts, "disk-sata2"); tl.Value != "38 °C" || tl.Title != "Disque 2" || tl.Size != 3 || !strings.Contains(tl.Sub2, "SMART PASSED") {
		t.Fatalf("%+v", tl)
	}
	if tl, _ := tileByID(ts, "nas-fans"); tl.Sub != "2 sur 2 en marche" {
		t.Fatalf("les ventilateurs 3 et 4 non montés ne comptent pas: %+v", tl)
	}
}

func TestTilesShowDiskDefects(t *testing.T) {
	in := input()
	in.NAS.Disks[1].Pending, in.NAS.Disks[1].Reported = 40, 1087
	in.NAS.Disks[0].Reallocated = 5
	ts := buildTiles(in, []finding{{Key: "k", Level: levelCrit, Text: "disque sata2"}}, defaultLimits())
	bad, _ := tileByID(ts, "disk-sata2")
	if bad.Level != "crit" || bad.Value != "38 °C" || !strings.Contains(bad.Sub2, "en attente 40") || !strings.Contains(bad.Sub2, "erreurs 1087") {
		t.Fatalf("%+v", bad)
	}
	watch, _ := tileByID(ts, "disk-sata1")
	if watch.Level != "warn" || !strings.Contains(watch.Sub2, "réalloués 5") {
		t.Fatalf("un disque avec des secteurs réalloués doit être à surveiller: %+v", watch)
	}
	if a, _ := tileByID(ts, "anomalies"); a.Level != "crit" || a.Value != "1" {
		t.Fatalf("%+v", a)
	}
}

func TestTilesRaidAndFans(t *testing.T) {
	in := input()
	in.NAS.RAID = []raidInfo{{Name: "md2", Level: "raid6", Total: 8, Up: 7, Status: "degraded", Rebuilding: true, Progress: 42}}
	in.NAS.NASFans = map[int]int{1: 0, 2: 1300}
	ts := buildTiles(in, nil, defaultLimits())
	if r, _ := tileByID(ts, "raid"); r.Level != "crit" || r.Value != "7/8" || !strings.Contains(r.Sub, "42 %") {
		t.Fatalf("%+v", r)
	}
	if f, _ := tileByID(ts, "nas-fans"); f.Level != "warn" || f.Sub != "1 sur 2 en marche" {
		t.Fatalf("%+v", f)
	}
}

func TestTilesWithoutNASData(t *testing.T) {
	in := input()
	in.HaveNAS = false
	ts := buildTiles(in, nil, defaultLimits())
	if len(ts) != 1 || ts[0].ID != "anomalies" {
		t.Fatalf("sans mesure NAS, seule la tuile des anomalies est attendue: %+v", ts)
	}
}

// ---- coupures brutales ----

func disksWithRetract(vals map[string]int) []diskInfo {
	var out []diskInfo
	for name, v := range vals {
		out = append(out, diskInfo{Name: name, Retract: v, RetractOK: true})
	}
	return out
}

func TestParseSmartEmergencyRetract(t *testing.T) {
	d := parseSmart("192 Emergency_Retract_Counnt 0x0032 100 100 000 Old_age Always - 13\n")
	if !d.RetractOK || d.Retract != 13 {
		t.Fatalf("%+v", d)
	}
}

func TestRetractEventsFirstRunIsSilent(t *testing.T) {
	evs, next := retractEvents(map[string]int{}, disksWithRetract(map[string]int{"sata1": 15, "sata2": 13}))
	if len(evs) != 0 {
		t.Fatalf("le premier relevé sert de référence: %+v", evs)
	}
	if next["sata1"] != 15 || next["sata2"] != 13 {
		t.Fatalf("%v", next)
	}
}

func TestRetractEventsDetectsIncrease(t *testing.T) {
	prev := map[string]int{"sata1": 15, "sata2": 13, "sata3": 13}
	evs, next := retractEvents(prev, disksWithRetract(map[string]int{"sata1": 16, "sata2": 13, "sata3": 15}))
	if len(evs) != 2 || evs[0].Disk != "sata1" || evs[0].Delta != 1 || evs[1].Disk != "sata3" || evs[1].Delta != 2 {
		t.Fatalf("%+v", evs)
	}
	if next["sata1"] != 16 || next["sata3"] != 15 {
		t.Fatalf("le repère doit avancer: %v", next)
	}
	if prev["sata1"] != 15 {
		t.Fatal("la table d'origine ne doit pas être modifiée")
	}
}

func TestRetractEventsReplacedDiskAndStandby(t *testing.T) {
	prev := map[string]int{"sata2": 13, "sata4": 12}
	disks := disksWithRetract(map[string]int{"sata2": 0}) // sata2 remplacé: compteur à zéro
	disks = append(disks, diskInfo{Name: "sata4", Standby: true})
	evs, next := retractEvents(prev, disks)
	if len(evs) != 0 {
		t.Fatalf("un disque remplacé ou en veille ne déclenche rien: %+v", evs)
	}
	if next["sata2"] != 0 || next["sata4"] != 12 {
		t.Fatalf("sata2 repart de zéro, sata4 garde son repère: %v", next)
	}
}

func TestPowerStateRoundTripAndMissingFile(t *testing.T) {
	path := t.TempDir() + "/etat.json"
	if got := loadPowerState(path); len(got) != 0 {
		t.Fatalf("fichier absent: table vide attendue: %v", got)
	}
	if err := savePowerState(path, map[string]int{"sata1": 15}); err != nil {
		t.Fatal(err)
	}
	if got := loadPowerState(path); got["sata1"] != 15 {
		t.Fatalf("%v", got)
	}
	if err := savePowerState("", map[string]int{"x": 1}); err != nil {
		t.Fatalf("sans fichier configuré, rien à écrire: %v", err)
	}
}

func TestPowerLossDetectedAcrossRestart(t *testing.T) {
	// Scénario: la surveillance voit 13, une coupure survient (la surveillance s'arrête avec le NAS),
	// puis elle redémarre et lit 14: la comparaison avec le fichier révèle la coupure.
	path := t.TempDir() + "/etat.json"
	_, next := retractEvents(map[string]int{}, disksWithRetract(map[string]int{"sata1": 13}))
	if err := savePowerState(path, next); err != nil {
		t.Fatal(err)
	}
	evs, _ := retractEvents(loadPowerState(path), disksWithRetract(map[string]int{"sata1": 14}))
	if len(evs) != 1 || evs[0].Delta != 1 {
		t.Fatalf("coupure pendant l'arrêt de la surveillance non détectée: %+v", evs)
	}
	// Un seul disque surveillé sur un seul: pas de comparaison possible, traité comme isolé.
	m := powerLossMessage(evs, 1, 5*time.Minute)
	if strings.Contains(m.Short, "sata1") || !strings.Contains(m.Body, "sata1 +1") {
		t.Fatalf("%+v", m)
	}
}

func TestIsGeneralPowerLoss(t *testing.T) {
	cases := []struct {
		affected, monitored int
		want                bool
	}{
		{1, 8, false}, // un seul disque sur huit: événement isolé (cas du 26/10/2026)
		{4, 8, false},
		{6, 8, true}, // trois quarts ou plus: coupure générale
		{8, 8, true},
		{1, 1, false}, // trop peu de disques pour conclure
	}
	for _, c := range cases {
		if got := isGeneralPowerLoss(c.affected, c.monitored); got != c.want {
			t.Errorf("%d/%d: obtenu %v, attendu %v", c.affected, c.monitored, got, c.want)
		}
	}
}

func TestPowerMessagesDistinguishIsolatedFromGeneral(t *testing.T) {
	one := []powerEvent{{Disk: "sata1", Delta: 1}}
	m := powerLossMessage(one, 8, 17*time.Hour+11*time.Minute)
	if m.Priority != 3 || strings.Contains(m.Subject, "coupure de courant") || !strings.Contains(m.Body, "Ce n'est pas une coupure générale") {
		t.Fatalf("un seul disque sur huit ne doit pas être présenté comme une coupure: %+v", m)
	}
	var all []powerEvent
	for _, n := range []string{"sata1", "sata2", "sata3", "sata4", "sata5", "sata6", "sata7", "sata8"} {
		all = append(all, powerEvent{Disk: n, Delta: 1})
	}
	g := powerLossMessage(all, 8, time.Minute)
	if g.Priority != 4 || !strings.Contains(g.Subject, "coupure de courant brutale") || !strings.Contains(g.Short, "8 disques sur 8") {
		t.Fatalf("%+v", g)
	}
}

func TestParseSmartStandby(t *testing.T) {
	d := parseSmart("Device is in STANDBY mode, exiting...\n")
	if !d.Standby || d.TempOK {
		t.Fatalf("un disque en veille ne doit rien mesurer: %+v", d)
	}
}

func TestParseSmartFallbackAirflowAndDefects(t *testing.T) {
	d := parseSmart("  5 Reallocated_Sector_Ct 0x0033 100 100 010 Pre-fail Always - 8\n" +
		"190 Airflow_Temperature 0x0022 062 046 040 Old_age Always - 41 (Min/Max 25/42)\n" +
		"197 Current_Pending_Sector 0x0012 100 100 000 Old_age Always - 3\n")
	if !d.TempOK || d.Temp != 41 || d.Reallocated != 8 || d.Pending != 3 {
		t.Fatalf("%+v", d)
	}
}

func TestParseUPS(t *testing.T) {
	u := parseUPS("battery.charge: 100\nbattery.runtime: 2003\nups.status: OL\nups.load: 12\ninput.voltage: 231.0\n")
	if !u.Present || u.Status != "OL" || u.Charge != 100 || u.RuntimeS != 2003 {
		t.Fatalf("%+v", u)
	}
	if parseUPS("Error: Data stale\n").Present {
		t.Fatal("sans ups.status, l'onduleur doit être absent")
	}
}

const mdstatOK = `Personalities : [raid1] [raid6] [raid5] [raid4] [raidF1]
md2 : active raid6 sata7p5[0] sata1p5[4] sata6p5[5] sata3p5[6] sata8p5[7] sata4p5[3] sata5p5[2] sata2p5[1]
      70248929280 blocks super 1.2 level 6, 64k chunk, algorithm 2 [8/8] [UUUUUUUU]

md1 : active raid1 sata8p2[0] sata6p2[7] sata3p2[6] sata7p2[5] sata1p2[4] sata4p2[3] sata5p2[2] sata2p2[1]
      2097088 blocks [8/8] [UUUUUUUU]

unused devices: <none>
`

func TestParseMdstat(t *testing.T) {
	r := parseMdstat(mdstatOK)
	if len(r) != 2 || r[0].Name != "md2" || r[0].Level != "raid6" || r[0].Up != 8 || r[0].Status != "ok" {
		t.Fatalf("%+v", r)
	}
	deg := strings.Replace(mdstatOK, "[8/8] [UUUUUUUU]\n\nmd1", "[8/7] [UUUUUUU_]\n\nmd1", 1)
	r = parseMdstat(deg)
	if r[0].Status != "degraded" || r[0].Up != 7 {
		t.Fatalf("RAID dégradé non détecté: %+v", r[0])
	}
	re := mdstatOK + "      [==>..................]  resync = 12.0% (1/8) finish=100.0min\n"
	if r = parseMdstat(re); r[len(r)-1].Status != "resync" {
		t.Fatalf("resynchronisation non détectée: %+v", r)
	}
}

func TestParseEnv(t *testing.T) {
	e := parseEnv("# commentaire\nNTFY_TOPIC=abc\r\nMAIL_TO = \"a@b.fr, c@d.fr\"\nVIDE=\nSANS_EGAL\n")
	if e["NTFY_TOPIC"] != "abc" || e["MAIL_TO"] != "a@b.fr, c@d.fr" || e["VIDE"] != "" {
		t.Fatalf("%v", e)
	}
}

func TestScrubHidesSecrets(t *testing.T) {
	s := scrub("Post https://x/sec-123/y: dial tcp: timeout", "sec-123", "")
	if strings.Contains(s, "sec-123") {
		t.Fatalf("secret visible: %s", s)
	}
}

// ---- assess ----

func healthyNAS() nasSnap {
	return nasSnap{
		At: time.Unix(1000, 0), CPU: 44, CPUOK: true,
		BoardTemp: map[string]float64{"temp1": 31, "temp2": 35},
		NASFans:   map[int]int{1: 1338, 2: 1333, 3: 0, 4: 0},
		Disks:     []diskInfo{{Name: "sata1", Temp: 32, TempOK: true, Health: "PASSED"}, {Name: "sata2", Temp: 38, TempOK: true, Health: "PASSED"}},
		UPS:       upsInfo{Present: true, Status: "OL", Charge: 100, RuntimeS: 2003},
		RAID:      []raidInfo{{Name: "md2", Level: "raid6", Total: 8, Up: 8, Status: "ok"}},
		VolUsed:   22, VolOK: true,
	}
}

func input() assessInput {
	return assessInput{
		NAS: healthyNAS(), HaveNAS: true, DevsOK: true, Enforce: true, NasCh: 13, QnapCh: 14, UnreachAt: 5 * time.Minute,
		Devs: []device{fan(1, 770), fan(2, 770), fan(3, 770), fan(15, 770), pr(13, 27), pr(14, 23)},
	}
}

func keys(fs []finding) string {
	var k []string
	for _, f := range fs {
		k = append(k, f.Key+":"+f.Level)
	}
	return strings.Join(k, ",")
}

func TestAssessHealthyIsSilent(t *testing.T) {
	if fs := assess(input(), defaultLimits()); len(fs) != 0 {
		t.Fatalf("aucune anomalie attendue: %s", keys(fs))
	}
}

func TestAssessUnpopulatedNASFansAreNotAFailure(t *testing.T) {
	// fan3 et fan4 de la carte sont à 0 car non montés: seuls fan1 et fan2 sont attendus.
	for _, f := range assess(input(), defaultLimits()) {
		t.Fatalf("faux positif: %v", f)
	}
}

func TestAssessDiskThresholds(t *testing.T) {
	in := input()
	// Limite voulue par l'utilisateur (28/10/2026): alerte à 40 °C, critique à 43 °C, arrêt à 45 °C.
	in.NAS.Disks[1].Temp = 39
	if got := keys(assess(in, defaultLimits())); got != "" {
		t.Fatalf("39 °C est encore normal: %s", got)
	}
	in.NAS.Disks[1].Temp = 41
	if got := keys(assess(in, defaultLimits())); got != "disk-temp-sata2:warn" {
		t.Fatalf("%s", got)
	}
	in.NAS.Disks[1].Temp = 44
	if got := keys(assess(in, defaultLimits())); got != "disk-temp-sata2:crit" {
		t.Fatalf("%s", got)
	}
}

func TestAssessSmartDefects(t *testing.T) {
	in := input()
	in.NAS.Disks[0].Pending = 2
	in.NAS.Disks[1].Health = "FAILED!"
	got := keys(assess(in, defaultLimits()))
	if !strings.Contains(got, "disk-bad-sata1:crit") || !strings.Contains(got, "disk-health-sata2:crit") {
		t.Fatalf("%s", got)
	}
}

func TestAssessNASFans(t *testing.T) {
	in := input()
	in.NAS.NASFans[2] = 0
	if got := keys(assess(in, defaultLimits())); got != "nasfan-2:warn" {
		t.Fatalf("un ventilateur: %s", got)
	}
	in.NAS.NASFans[1] = 0
	got := keys(assess(in, defaultLimits()))
	if !strings.Contains(got, "nasfan-all:crit") {
		t.Fatalf("tous arrêtés: %s", got)
	}
}

func TestAssessUPSRAIDVolume(t *testing.T) {
	in := input()
	in.NAS.UPS.Status = "OB DISCHRG"
	in.NAS.RAID[0].Status, in.NAS.RAID[0].Up = "degraded", 7
	in.NAS.VolUsed = 96
	got := keys(assess(in, defaultLimits()))
	for _, want := range []string{"ups:warn", "raid-md2:crit", "volume:crit"} {
		if !strings.Contains(got, want) {
			t.Fatalf("manque %s dans %s", want, got)
		}
	}
	in.NAS.UPS.Status = "OB LB"
	if !strings.Contains(keys(assess(in, defaultLimits())), "ups:crit") {
		t.Fatal("batterie faible doit être critique")
	}
}

func TestAssessCorsair(t *testing.T) {
	in := input()
	in.Devs = []device{fan(1, 770), fan(2, 0), fan(3, 770), pr(13, 36), pr(14, 23)}
	got := keys(assess(in, defaultLimits()))
	for _, want := range []string{"corsair-fan-2:warn", "corsair-missing:warn", "probe-NAS:warn"} {
		if !strings.Contains(got, want) {
			t.Fatalf("manque %s dans %s", want, got)
		}
	}
	in.Devs = []device{fan(1, 0), fan(2, 0), fan(3, 0), fan(15, 0), pr(13, 43), pr(14, 23)}
	got = keys(assess(in, defaultLimits()))
	if !strings.Contains(got, "corsair-all:crit") || !strings.Contains(got, "probe-NAS:crit") {
		t.Fatalf("%s", got)
	}
}

func TestAssessGraceSuppressesMeubleChecks(t *testing.T) {
	in := input()
	in.Enforce = false
	in.Devs = []device{}
	if got := keys(assess(in, defaultLimits())); got != "" {
		t.Fatalf("pendant la grâce, pas d'alerte sur le meuble: %s", got)
	}
}

func TestAssessAPIDown(t *testing.T) {
	in := input()
	in.DevsOK = false
	in.APIDownFor = time.Minute
	if got := keys(assess(in, defaultLimits())); got != "" {
		t.Fatalf("trop tôt: %s", got)
	}
	in.APIDownFor = 6 * time.Minute
	if got := keys(assess(in, defaultLimits())); got != "api:warn" {
		t.Fatalf("%s", got)
	}
}

func TestAssessStaleNAS(t *testing.T) {
	in := input()
	in.NASAge = 10 * time.Minute
	if got := keys(assess(in, defaultLimits())); got != "nas-stale:warn" {
		t.Fatalf("%s", got)
	}
}

// ---- règles d'arrêt thermiques ----

func TestHeatSustainedCountsOncePerReading(t *testing.T) {
	h := &heat{label: "disque sata2", sustained: 55, instant: 58}
	hold := 5 * time.Minute
	t0 := time.Unix(0, 0)
	// Un seul relevé à 59 °C (au-dessus du seuil immédiat), que la boucle principale relit toutes
	// les 10 s pendant la minute qui sépare deux relevés: il ne doit compter qu'une fois.
	for i := 0; i < 6; i++ {
		if r := h.update(59, t0, t0.Add(time.Duration(i)*10*time.Second), hold); r != "" {
			t.Fatalf("relecture %d: arrêt prématuré: %s", i, r)
		}
	}
}

func TestHeatInstantNeedsTwoDistinctReadings(t *testing.T) {
	h := &heat{label: "disque", sustained: 55, instant: 58}
	t0 := time.Unix(0, 0)
	if r := h.update(60, t0, t0, 5*time.Minute); r != "" {
		t.Fatalf("un relevé isolé ne doit pas éteindre: %s", r)
	}
	if r := h.update(60, t0, t0.Add(10*time.Second), 5*time.Minute); r != "" {
		t.Fatalf("le même relevé relu ne compte pas: %s", r)
	}
	if r := h.update(60, t0.Add(time.Minute), t0.Add(time.Minute), 5*time.Minute); r == "" {
		t.Fatal("deux relevés distincts au-dessus du seuil immédiat doivent éteindre")
	}
}

func TestHeatSustainedAfterHold(t *testing.T) {
	h := &heat{label: "disque", sustained: 55, instant: 58}
	t0 := time.Unix(0, 0)
	for i := 0; i < 5; i++ {
		at := t0.Add(time.Duration(i) * time.Minute)
		if r := h.update(56, at, at, 5*time.Minute); r != "" {
			t.Fatalf("minute %d: %s", i, r)
		}
	}
	at := t0.Add(5 * time.Minute)
	if r := h.update(56, at, at, 5*time.Minute); r == "" {
		t.Fatal("5 minutes au-dessus du seuil doivent éteindre")
	}
}

func TestHeatResets(t *testing.T) {
	h := &heat{label: "disque", sustained: 55, instant: 58}
	t0 := time.Unix(0, 0)
	h.update(56, t0, t0, 5*time.Minute)
	t1 := t0.Add(3 * time.Minute)
	h.update(50, t1, t1, 5*time.Minute)
	t2 := t0.Add(6 * time.Minute)
	if r := h.update(56, t2, t2, 5*time.Minute); r != "" {
		t.Fatalf("le compteur aurait dû repartir de zéro: %s", r)
	}
}

func TestNasCheckHonoursGraceAndShutsDown(t *testing.T) {
	m := newMon()
	hot := healthyNAS()
	hot.Disks[0].Temp = 60
	hot.At = time.Unix(10, 0)
	if r := m.nasCheck(hot, at(10)); r != "" {
		t.Fatalf("arrêt pendant le délai de grâce: %s", r)
	}
	hot.At = time.Unix(200, 0)
	m.nasCheck(hot, at(200))
	hot.At = time.Unix(260, 0)
	if r := m.nasCheck(hot, at(260)); r == "" {
		t.Fatal("un disque à 60 °C sur deux relevés doit éteindre le NAS")
	}
}

func TestNasCheckNormalDoesNotShutDown(t *testing.T) {
	m := newMon()
	s := healthyNAS()
	for i := 0; i < 100; i++ {
		s.At = time.Unix(int64(300+i*60), 0)
		if r := m.nasCheck(s, s.At); r != "" {
			t.Fatalf("arrêt inattendu: %s", r)
		}
	}
}

// ---- alertes ----

type capture struct{ msgs []message }

func (c *capture) post(m message) { c.msgs = append(c.msgs, m) }

func warn(key, text string) finding { return finding{Key: key, Level: levelWarn, Text: text} }

func TestAlerterConfirmationAndNoSpam(t *testing.T) {
	c := &capture{}
	a := newAlerter(c.post)
	f := []finding{warn("disk-temp-sata2", "disque sata2 à 46 °C")}
	a.reconcile(at(0), f)
	if len(c.msgs) != 0 {
		t.Fatal("une anomalie doit durer avant d'être notifiée")
	}
	a.reconcile(at(70), f)
	if len(c.msgs) != 1 || c.msgs[0].Priority != 4 {
		t.Fatalf("une notification attendue: %+v", c.msgs)
	}
	for s := 80; s < 3600; s += 10 {
		a.reconcile(at(s), f)
	}
	if len(c.msgs) != 1 {
		t.Fatalf("la même alerte ne doit pas être répétée dans l'heure: %d messages", len(c.msgs))
	}
}

func TestAlerterReminderAndEscalation(t *testing.T) {
	c := &capture{}
	a := newAlerter(c.post)
	w := []finding{warn("k", "texte")}
	a.reconcile(at(0), w)
	a.reconcile(at(70), w)
	cr := []finding{{Key: "k", Level: levelCrit, Text: "texte grave"}}
	a.reconcile(at(100), cr)
	if len(c.msgs) != 2 || c.msgs[1].Priority != 5 {
		t.Fatalf("l'aggravation doit être notifiée tout de suite: %+v", c.msgs)
	}
	a.reconcile(at(100+3600+1), cr)
	if len(c.msgs) != 3 {
		t.Fatalf("une alerte critique persistante doit être rappelée au bout d'une heure: %d", len(c.msgs))
	}
}

func TestAlerterRecovery(t *testing.T) {
	c := &capture{}
	a := newAlerter(c.post)
	f := []finding{warn("k", "disque sata2 à 46 °C")}
	a.reconcile(at(0), f)
	a.reconcile(at(70), f)
	a.reconcile(at(80), nil) // disparu
	if len(c.msgs) != 1 {
		t.Fatal("pas de message de retour avant la période de confirmation")
	}
	a.reconcile(at(80+130), nil)
	if len(c.msgs) != 2 || !strings.Contains(c.msgs[1].Body, "RÉTABLI") {
		t.Fatalf("retour à la normale attendu: %+v", c.msgs)
	}
	if len(a.activeKeys()) != 0 {
		t.Fatal("l'alerte aurait dû être oubliée")
	}
}

func TestAlerterTransientIsSilent(t *testing.T) {
	c := &capture{}
	a := newAlerter(c.post)
	a.reconcile(at(0), []finding{warn("k", "pic")})
	a.reconcile(at(20), nil)
	a.reconcile(at(400), nil)
	if len(c.msgs) != 0 {
		t.Fatalf("un pic de moins d'une minute ne doit rien envoyer: %+v", c.msgs)
	}
}

func TestAlerterGroupsMessages(t *testing.T) {
	c := &capture{}
	a := newAlerter(c.post)
	f := []finding{warn("a", "A"), warn("b", "B"), warn("c", "C")}
	a.reconcile(at(0), f)
	a.reconcile(at(70), f)
	if len(c.msgs) != 1 || !strings.Contains(c.msgs[0].Subject, "3 alertes") {
		t.Fatalf("un seul message groupé attendu: %+v", c.msgs)
	}
}

func TestDigesterOncePerSlot(t *testing.T) {
	d := newDigester([]int{8, 13, 19})
	day := time.Date(2026, 10, 26, 0, 0, 0, 0, time.Local)
	if d.due(day.Add(7*time.Hour + 59*time.Minute)) {
		t.Fatal("trop tôt")
	}
	if !d.due(day.Add(8 * time.Hour)) {
		t.Fatal("8 h attendu")
	}
	if d.due(day.Add(8*time.Hour + 30*time.Minute)) {
		t.Fatal("un seul résumé par créneau")
	}
	if !d.due(day.Add(13 * time.Hour)) {
		t.Fatal("13 h attendu")
	}
	if !d.due(day.Add(24*time.Hour + 8*time.Hour)) {
		t.Fatal("le lendemain à 8 h doit repartir")
	}
}

func TestParseHours(t *testing.T) {
	h := parseHours("8, 13,19, 99, x")
	if len(h) != 3 || h[0] != 8 || h[2] != 19 {
		t.Fatalf("%v", h)
	}
}

func TestDigestMessage(t *testing.T) {
	in := input()
	h := newHistory()
	h.add(at(0), metrics(in, 13, 14))
	m := digestMessage(at(100), in, nil, h, true, 13, 14)
	if !strings.Contains(m.Short, "tout est normal") || !strings.Contains(m.Body, "mode à blanc") {
		t.Fatalf("%+v", m)
	}
	if strings.Contains(m.Short, "192.168") || strings.Contains(m.Short, "B855FDBE") {
		t.Fatal("le texte ntfy ne doit contenir ni adresse ni numéro de série")
	}
	m = digestMessage(at(100), in, []finding{warn("k", "x")}, h, false, 13, 14)
	if m.Priority != 4 || !strings.Contains(m.Short, "1 anomalie") {
		t.Fatalf("%+v", m)
	}
}

// Sortie réelle de `synodisk --enum -t internal` relevée le 2026-10-27 après un démarrage où les noms
// noyau sata1↔sata2 et sata3↔sata4 étaient croisés par rapport aux baies de DSM.
const synodiskCroise = `************ Disk Info ***************
>> Disk id: 2
>> Slot id: -1
>> Disk path: /dev/sata1
>> Disk model: ST12000VN0008-2JH101
************ Disk Info ***************
>> Disk id: 1
>> Slot id: -1
>> Disk path: /dev/sata2
>> Disk model: ST12000NE0008-2PK103
************ Disk Info ***************
>> Disk id: 4
>> Slot id: -1
>> Disk path: /dev/sata3
************ Disk Info ***************
>> Disk id: 3
>> Slot id: -1
>> Disk path: /dev/sata4
`

func TestBayNamesFollowDsmBayNotKernelName(t *testing.T) {
	devs := []string{"/dev/sata1", "/dev/sata2", "/dev/sata3", "/dev/sata4"}
	names := bayNames(devs, parseSynodisk(synodiskCroise))
	want := map[string]string{"/dev/sata1": "sata2", "/dev/sata2": "sata1", "/dev/sata3": "sata4", "/dev/sata4": "sata3"}
	for dev, n := range want {
		if names[dev] != n {
			t.Fatalf("%s: %q, attendu %q (%v)", dev, names[dev], n, names)
		}
	}
}

func TestBayNamesFallBackWhenTableIsIncompleteOrAmbiguous(t *testing.T) {
	devs := []string{"/dev/sata1", "/dev/sata2"}
	if names := bayNames(devs, parseSynodisk("")); names != nil {
		t.Fatalf("sortie vide: pas de table attendue, reçu %v", names)
	}
	if names := bayNames(devs, map[string]int{"/dev/sata1": 1}); names != nil {
		t.Fatalf("table incomplète: pas de table attendue, reçu %v", names)
	}
	if names := bayNames(devs, map[string]int{"/dev/sata1": 1, "/dev/sata2": 1}); names != nil {
		t.Fatalf("numéros en double: pas de table attendue, reçu %v", names)
	}
}

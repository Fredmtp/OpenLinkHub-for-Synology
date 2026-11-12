package main

import (
	"testing"
	"time"
)

var testCfg = alarmCfg{Gap: 300 * time.Millisecond, Count: 5, Pause: 2 * time.Second, Notice: 5 * time.Minute}

func TestAlarmLevelOf(t *testing.T) {
	cases := []struct {
		name string
		fs   []finding
		want alarmLevel
	}{
		{"rien", nil, alarmNone},
		{"disque à 41 °C: notification seulement", []finding{{Key: "disk-temp-sata1", Level: levelWarn}}, alarmNone},
		{"disque critique", []finding{{Key: "disk-temp-sata1", Level: levelCrit}}, alarmAlert},
		{"processeur critique", []finding{{Key: "cpu-temp", Level: levelCrit}}, alarmAlert},
		{"sonde du meuble critique", []finding{{Key: "probe-NAS", Level: levelCrit}}, alarmAlert},
		{"sonde absente: pas de son", []finding{{Key: "probe-NAS-missing", Level: levelWarn}}, alarmNone},
		{"ventilation dégradée ou perdue", []finding{{Key: "corsair-extraction", Level: levelCrit}}, alarmAlert},
		{"tous arrêtés", []finding{{Key: "corsair-all", Level: levelCrit}}, alarmAlert},
		{"un ventilateur arrêté", []finding{{Key: "corsair-fan-2", Level: levelWarn}}, alarmNotice},
		{"SMART: pas de son", []finding{{Key: "disk-bad-sata1", Level: levelCrit}, {Key: "raid-md2", Level: levelWarn}}, alarmNone},
		{"l'alerte l'emporte sur l'avis", []finding{{Key: "corsair-fan-2", Level: levelWarn}, {Key: "disk-temp-sata1", Level: levelCrit}}, alarmAlert},
	}
	for _, c := range cases {
		if got := alarmLevelOf(c.fs); got != c.want {
			t.Errorf("%s: niveau %d, attendu %d", c.name, got, c.want)
		}
	}
}

// Le scénario de l'utilisateur: un disque chauffe alors que la ventilation tourne.
func TestHotDiskWithVentilationOnRaisesAlert(t *testing.T) {
	in := input()
	in.NAS.Disks[1].Temp = 43
	if got := alarmLevelOf(assess(in, defaultLimits())); got != alarmAlert {
		t.Fatalf("disque à 43 °C, ventilation normale: alerte sonore attendue, niveau %d", got)
	}
	in.NAS.Disks[1].Temp = 41
	if got := alarmLevelOf(assess(in, defaultLimits())); got != alarmNone {
		t.Fatalf("disque à 41 °C: pas de son, niveau %d", got)
	}
}

// Le scénario de l'incident: la ventilation tombe, les disques sont encore froids.
func TestVentilationFailureRaisesAlertBeforeDisksHeat(t *testing.T) {
	in := input()
	in.Devs = []device{fan(1, 0), fan(2, 0), fan(3, 770), fan(15, 770), pr(13, 27), pr(14, 23)}
	if got := alarmLevelOf(assess(in, defaultLimits())); got != alarmAlert {
		t.Fatalf("deux extracteurs arrêtés: alerte sonore attendue, niveau %d", got)
	}
	in.Devs = []device{fan(1, 0), fan(2, 770), fan(3, 770), fan(15, 770), pr(13, 27), pr(14, 23)}
	if got := alarmLevelOf(assess(in, defaultLimits())); got != alarmNotice {
		t.Fatalf("un extracteur arrêté: avis attendu, niveau %d", got)
	}
	in.Maintenance = true
	if got := alarmLevelOf(assess(in, defaultLimits())); got != alarmNone {
		t.Fatalf("maintenance: silence attendu, niveau %d", got)
	}
}

func TestAlarmTickAlertPlaysFiveBeepsThenPauses(t *testing.T) {
	beeps, sleeps := 0, []time.Duration{}
	var last time.Time
	wait := alarmTick(alarmAlert, false, time.Unix(1000, 0), &last, testCfg,
		func() { beeps++ }, func(d time.Duration) { sleeps = append(sleeps, d) })
	if beeps != 5 {
		t.Fatalf("%d bips, attendu 5", beeps)
	}
	if len(sleeps) != 4 {
		t.Fatalf("%d intervalles, attendu 4 (pas d'attente après le dernier bip)", len(sleeps))
	}
	for _, d := range sleeps {
		if d != 300*time.Millisecond {
			t.Fatalf("intervalle %s, attendu 0,3 s", d)
		}
	}
	if wait != 2*time.Second {
		t.Fatalf("silence de %s, attendu 2 s", wait)
	}
}

func TestAlarmTickNoticeBeepsOnceThenEveryFiveMinutes(t *testing.T) {
	beeps := 0
	beep := func() { beeps++ }
	nosleep := func(time.Duration) {}
	var last time.Time
	t0 := time.Unix(1000, 0)
	alarmTick(alarmNotice, false, t0, &last, testCfg, beep, nosleep)
	if beeps != 1 {
		t.Fatalf("premier bip attendu tout de suite: %d", beeps)
	}
	alarmTick(alarmNotice, false, t0.Add(4*time.Minute), &last, testCfg, beep, nosleep)
	if beeps != 1 {
		t.Fatalf("pas de bip avant 5 min: %d", beeps)
	}
	alarmTick(alarmNotice, false, t0.Add(5*time.Minute), &last, testCfg, beep, nosleep)
	if beeps != 2 {
		t.Fatalf("bip attendu à 5 min: %d", beeps)
	}
	// Retour à la normale puis nouvelle panne: le bip repart tout de suite.
	alarmTick(alarmNone, false, t0.Add(6*time.Minute), &last, testCfg, beep, nosleep)
	alarmTick(alarmNotice, false, t0.Add(7*time.Minute), &last, testCfg, beep, nosleep)
	if beeps != 3 {
		t.Fatalf("nouvelle panne: bip immédiat attendu: %d", beeps)
	}
}

func TestAlarmTickNoticeDisabled(t *testing.T) {
	beeps := 0
	var last time.Time
	c := testCfg
	c.Notice = 0
	alarmTick(alarmNotice, false, time.Unix(1000, 0), &last, c, func() { beeps++ }, func(time.Duration) {})
	if beeps != 0 {
		t.Fatalf("avis désactivé: %d bip(s)", beeps)
	}
}

func TestAlarmTickSilencedPlaysNothing(t *testing.T) {
	beeps := 0
	var last time.Time
	for _, lvl := range []alarmLevel{alarmAlert, alarmNotice} {
		alarmTick(lvl, true, time.Unix(1000, 0), &last, testCfg, func() { beeps++ }, func(time.Duration) {})
	}
	if beeps != 0 {
		t.Fatalf("silence demandé: %d bip(s)", beeps)
	}
}

package main

import (
	"strings"
	"testing"
)

const mdstatScrub = `Personalities : [raid1] [raid6] [raid5] [raid4] [raidF1]
md2 : active raid6 sata7p5[0] sata2p5[4] sata6p5[5] sata3p5[6] sata8p5[7] sata4p5[3] sata5p5[2] sata1p5[1]
      70248929280 blocks super 1.2 level 6, 64k chunk, algorithm 2 [8/8] [UUUUUUUU]
      [====>................]  check = 23.9% (2800000000/11708154880) finish=1020.2min speed=145000K/sec

md1 : active raid1 sata1p2[0] sata2p2[1]
      2097088 blocks [8/8] [UUUUUUUU]

unused devices: <none>
`

func TestParseMdstatNettoyageNEstPasUnePanne(t *testing.T) {
	rs := parseMdstat(mdstatScrub)
	var md2 *raidInfo
	for i := range rs {
		if rs[i].Name == "md2" {
			md2 = &rs[i]
		}
	}
	if md2 == nil {
		t.Fatal("md2 absent")
	}
	if !md2.Scrubbing || md2.Progress != 23.9 {
		t.Errorf("nettoyage attendu à 23,9 %%, reçu %+v", *md2)
	}
	if md2.Status != "ok" || md2.Rebuilding {
		t.Errorf("un nettoyage ne rend pas le RAID suspect: %+v", *md2)
	}
}

func TestParseMdstatReconstructionResteUneReconstruction(t *testing.T) {
	txt := strings.Replace(mdstatScrub, "check = 23.9%", "recovery = 12.5%", 1)
	rs := parseMdstat(txt)
	if rs[0].Scrubbing || !rs[0].Rebuilding || rs[0].Status != "resync" {
		t.Errorf("reconstruction attendue: %+v", rs[0])
	}
}

func scrubSnap(scrubbing bool, pct float64) nasSnap {
	return nasSnap{RAID: []raidInfo{{Name: "md2", Level: "raid6", Total: 8, Up: 8, Status: "ok",
		Scrubbing: scrubbing, Progress: pct, Mismatch: 0, MismatchOK: true}}}
}

func TestScrubDebutProgressionFin(t *testing.T) {
	w := &scrubWatch{}
	if n := w.observe(scrubSnap(false, 0), at(0)); n.Kind != "" {
		t.Fatalf("rien à signaler au repos: %+v", n)
	}
	n := w.observe(scrubSnap(true, 0.1), at(60))
	if n.Kind != "start" || !strings.Contains(n.Text, "démarré") || n.Msg.Priority != 2 {
		t.Fatalf("début attendu discret: %+v", n)
	}
	if n := w.observe(scrubSnap(true, 50), at(3600)); n.Kind != "" {
		t.Fatalf("pas de message en cours de route: %+v", n)
	}
	w.observe(scrubSnap(true, 99.8), at(80000))
	n = w.observe(scrubSnap(false, 0), at(80100))
	if n.Kind != "end" || !strings.Contains(n.Text, "terminé") || !strings.Contains(n.Text, "aucune incohérence") || !strings.Contains(n.Text, "durée mesurée 22 h") {
		t.Fatalf("fin attendue avec durée et résultat: %+v", n)
	}
	if n.Msg.Priority != 3 {
		t.Errorf("fin normale: priorité 3, reçu %d", n.Msg.Priority)
	}
}

func TestScrubFinAvecIncoherences(t *testing.T) {
	w := &scrubWatch{}
	w.observe(scrubSnap(true, 0.1), at(0))
	w.observe(scrubSnap(true, 99), at(1000))
	end := scrubSnap(false, 0)
	end.RAID[0].Mismatch = 128
	n := w.observe(end, at(1100))
	if n.Kind != "end" || n.Msg.Priority != 4 || !strings.Contains(n.Text, "128 incohérence") {
		t.Fatalf("incohérences: alerte de priorité 4 attendue, reçu %+v", n)
	}
}

func TestScrubInterrompuNEstPasTermine(t *testing.T) {
	w := &scrubWatch{}
	w.observe(scrubSnap(true, 0.1), at(0))
	w.observe(scrubSnap(true, 40), at(1000))
	n := w.observe(scrubSnap(false, 0), at(1100)) // suspendu dans DSM
	if n.Kind != "end" || !strings.Contains(n.Text, "interrompu") || strings.Contains(n.Text, "terminé") {
		t.Fatalf("nettoyage disparu à 40 %%: « interrompu » attendu, reçu %+v", n)
	}
}

func TestScrubSurveillanceDemarreeEnCoursDeRoute(t *testing.T) {
	w := &scrubWatch{}
	n := w.observe(scrubSnap(true, 60), at(0)) // la surveillance (re)démarre pendant un nettoyage
	if n.Kind != "start" || !strings.Contains(n.Text, "en cours (60 %)") {
		t.Fatalf("début « en cours » attendu: %+v", n)
	}
	w.observe(scrubSnap(true, 99.9), at(5000))
	end := w.observe(scrubSnap(false, 0), at(5100))
	if !strings.Contains(end.Text, "suivi depuis") {
		t.Errorf("durée inconnue: « suivi depuis » attendu, reçu %q", end.Text)
	}
}

func TestScrubRaidDegradeALaFinEstSignale(t *testing.T) {
	w := &scrubWatch{}
	w.observe(scrubSnap(true, 0.1), at(0))
	w.observe(scrubSnap(true, 98), at(1000))
	end := scrubSnap(false, 0)
	end.RAID[0].Status, end.RAID[0].Up = "degraded", 7
	n := w.observe(end, at(1100))
	if n.Msg.Priority != 4 || !strings.Contains(n.Text, "degraded") {
		t.Fatalf("RAID dégradé à la fin: priorité 4 attendue, reçu %+v", n)
	}
}

func TestTuileRaidAfficheLeNettoyage(t *testing.T) {
	in := assessInput{NAS: scrubSnap(true, 23.9), HaveNAS: true}
	for _, tl := range buildTiles(in, nil, defaultLimits()) {
		if tl.ID == "raid" {
			if !strings.Contains(tl.Sub, "nettoyage des données 24 %") || tl.Level != "ok" {
				t.Errorf("tuile RAID inattendue: %+v", tl)
			}
			return
		}
	}
	t.Fatal("tuile RAID absente")
}

func TestRecordRowColonneNettoyage(t *testing.T) {
	row := recordRow(scrubSnap(true, 23.9), true, nil, false, defaultLimits(), 13, 14, false)
	if got := cell(row, "nettoyage_pct"); got != "23.9" {
		t.Errorf("nettoyage_pct = %q, attendu 23.9", got)
	}
	row = recordRow(scrubSnap(false, 0), true, nil, false, defaultLimits(), 13, 14, false)
	if got := cell(row, "nettoyage_pct"); got != "0" {
		t.Errorf("hors nettoyage: nettoyage_pct = %q, attendu 0", got)
	}
}

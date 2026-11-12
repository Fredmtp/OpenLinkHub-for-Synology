package main

import (
	"strings"
	"testing"
	"time"
)

// Relevé réel du DS1825+ le 02/11/2026: la baie est vue comme un seul périphérique « TR-004 DISK00 » sur scsi12.
const scsiReel = `Attached devices:
Host: scsi9 Channel: 00 Id: 00 Lun: 00
  Vendor: ATA      Model: ST12000NE0008-2PK103     Rev: CNA3
  Type:   Direct-Access                    ANSI  SCSI revision: 05
Host: scsi12 Channel: 00 Id: 00 Lun: 00
  Vendor: QNAP     Model: TR-004 DISK00            Rev: 6111
  Type:   Direct-Access                    ANSI  SCSI revision: 06
Host: scsi11 Channel: 00 Id: 00 Lun: 00
  Vendor: Synology Model: DiskStation              Rev: PMAP
  Type:   Direct-Access                    ANSI  SCSI revision: 06
`

func TestParseQnapScsiTrouveLaBaie(t *testing.T) {
	ok, host := parseQnapScsi(scsiReel, "QNAP")
	if !ok || host != "12" {
		t.Fatalf("baie attendue sur scsi12, reçu %v %q", ok, host)
	}
}

func TestParseQnapScsiAbsente(t *testing.T) {
	sans := strings.Replace(scsiReel, "QNAP    ", "Other   ", 1)
	if ok, _ := parseQnapScsi(sans, "QNAP"); ok {
		t.Fatal("baie débranchée: ne doit pas être trouvée")
	}
	if ok, _ := parseQnapScsi(scsiReel, ""); ok {
		t.Fatal("contrôle désactivé (fabricant vide): rien à trouver")
	}
}

const dmesgBaie = `[ 100.1] usb 3-2: new SuperSpeed USB device number 3 using xhci_hcd
[ 100.9] scsi host12: uas
[ 101.0] sd 12:0:0:0: [sdl] 23437770752 512-byte logical blocks
[ 500.0] sd 12:0:0:0: [sdl] tag#3 uas_eh_abort_handler 0 uas-tag 1 inflight: CMD IN
[ 500.1] sd 12:0:0:0: [sdl] tag#3 FAILED Result: hostbyte=DID_ERROR driverbyte=DRIVER_OK
[ 500.2] blk_update_request: I/O error, dev sdl, sector 12345
[ 600.0] sd 4:0:0:0: [sdd] tag#1 FAILED Result: hostbyte=DID_ERROR driverbyte=DRIVER_OK
[ 700.0] usb 3-1: reset full-speed USB device number 2 using xhci_hcd
`

func TestCountQnapErrorsNeCompteQueLaBaie(t *testing.T) {
	// 2 lignes concernent sd 12:… (abort et FAILED); l'erreur de sd 4 (autre disque) et le reset du Hub (usb 3-1) sont ignorés.
	if n := countQnapErrors(dmesgBaie, "12"); n != 2 {
		t.Fatalf("2 erreurs attendues pour scsi12, reçu %d", n)
	}
	if n := countQnapErrors(dmesgBaie, ""); n != 0 {
		t.Fatalf("hôte inconnu: 0, reçu %d", n)
	}
	// « sd 1:… » ne doit pas être confondu avec « sd 12:… »
	if n := countQnapErrors(dmesgBaie, "1"); n != 0 {
		t.Fatalf("scsi1 n'est pas scsi12: 0 attendu, reçu %d", n)
	}
}

func TestQnapAbsenceAlerteApres5Min(t *testing.T) {
	var q qnapTrack
	lim := defaultLimits()
	q.update(qnapSnap{Checked: true, Present: true, Host: "12"}, at(0))
	q.update(qnapSnap{Checked: true}, at(60)) // la baie disparaît
	if f := qnapFindings(q.view(at(60+4*60)), true, false, lim); len(f) != 0 {
		t.Fatalf("pas d'alerte avant 5 min: %+v", f)
	}
	f := qnapFindings(q.view(at(60+6*60)), true, false, lim)
	if len(f) != 1 || f[0].Key != "qnap-missing" {
		t.Fatalf("alerte d'absence attendue après 5 min: %+v", f)
	}
	// Retour de la baie: l'alerte disparaît.
	q.update(qnapSnap{Checked: true, Present: true, Host: "12"}, at(60+7*60))
	if f := qnapFindings(q.view(at(60+8*60)), true, false, lim); len(f) != 0 {
		t.Fatalf("baie revenue: plus d'alerte: %+v", f)
	}
}

func TestQnapNouvellesErreursAlerteEtSEteint(t *testing.T) {
	var q qnapTrack
	lim := defaultLimits()
	q.update(qnapSnap{Checked: true, Present: true, Host: "12", Errors: 5}, at(0)) // anciennes erreurs: référence
	if f := qnapFindings(q.view(at(60)), true, false, lim); len(f) != 0 {
		t.Fatalf("les erreurs déjà présentes au démarrage ne comptent pas: %+v", f)
	}
	q.update(qnapSnap{Checked: true, Present: true, Host: "12", Errors: 8}, at(120))
	f := qnapFindings(q.view(at(180)), true, false, lim)
	if len(f) != 1 || f[0].Key != "qnap-io" || !strings.Contains(f[0].Text, "3 nouvelle") {
		t.Fatalf("3 nouvelles erreurs: alerte attendue, reçu %+v", f)
	}
	if f := qnapFindings(q.view(at(120+int((31*time.Minute).Seconds()))), true, false, lim); len(f) != 0 {
		t.Fatalf("30 min sans nouvelle erreur: l'alerte s'éteint, reçu %+v", f)
	}
}

func TestQnapMaintenanceEtDelaiDeGraceEtDesactivation(t *testing.T) {
	var q qnapTrack
	lim := defaultLimits()
	q.update(qnapSnap{Checked: true}, at(0))
	late := q.view(at(3600))
	if f := qnapFindings(late, true, true, lim); len(f) != 0 {
		t.Fatalf("maintenance: pas d'alerte, reçu %+v", f)
	}
	if f := qnapFindings(late, false, false, lim); len(f) != 0 {
		t.Fatalf("délai de grâce du démarrage: pas d'alerte, reçu %+v", f)
	}
	var off qnapTrack
	off.update(qnapSnap{}, at(0)) // Checked = false: surveillance désactivée
	if v := off.view(at(3600)); v.Tracked {
		t.Fatalf("désactivée (-qnap-vendor vide): rien à suivre, reçu %+v", v)
	}
}

func TestQnapCarteExplicative(t *testing.T) {
	c := describeFinding(finding{Key: "qnap-missing", Level: levelWarn, Text: "baie QNAP absente depuis 6 min"}, assessInput{})
	if !strings.Contains(c.Title, "QNAP") || !strings.Contains(c.Action, "USB") || c.Gravity != "moyenne" {
		t.Errorf("carte inattendue: %+v", c)
	}
}

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Alerte lumineuse sur les ventilateurs du meuble (décisions du 28/10/2026). Les LED ne fonctionnent
// que si les ventilateurs sont alimentés: le bip du NAS reste le signal principal.
//
// Règles, appliquées à chaque seconde d'après les anomalies en cours (voir assess):
//   - un ventilateur arrêté mais alimenté (corsair-fan-N): CE ventilateur seul s'allume;
//   - ventilation dégradée ou perdue (corsair-extraction, corsair-all): les 4 ventilateurs s'allument;
//   - un disque au niveau critique: le ventilateur d'extraction de sa zone s'allume (baies 1 à 3 =
//     gauche, 4 et 5 = centre, 6 à 8 = droite, voir -led-zones);
//   - le NAS chauffe (processeur, carte mère ou sonde du meuble au niveau critique): les trois
//     extracteurs s'allument EN ALTERNANCE, de gauche à droite, toutes les -led-alternate (3 s), et
//     l'admission reste normale.
//
// « S'allumer » = prendre le profil de lumière -led-profile (« circle »). OpenLinkHub écrit le profil sur
// disque à chaque changement: on ne change donc que ce qui doit l'être, et jamais sans anomalie. Le
// profil d'origine de chaque ventilateur est gardé en mémoire ET dans le fichier -led-state, pour être
// remis même si la surveillance s'arrête brutalement. Les LED s'allument aussi en mode à blanc: cela ne
// coupe rien.

var (
	ledsOn         = flag.Bool("leds", false, "alerte lumineuse sur les LED des ventilateurs du meuble (profil -led-profile)")
	ledProfile     = flag.String("led-profile", "circle", "profil de lumière d'alerte")
	ledNormal      = flag.String("led-normal", "probe-temperature", "profil de lumière remis si l'original est introuvable")
	ledZonesFlag   = flag.String("led-zones", "1-3:1,4-5:3,6-8:2", "zones: baies DSM de-à:canal du ventilateur le plus proche (relevé du 28/10/2026)")
	ledAlternate   = flag.Duration("led-alternate", 3*time.Second, "durée d'allumage de chaque extracteur dans l'alternance « le NAS chauffe »")
	ledMaintenance = flag.String("led-maintenance", "static", "profil de lumière des 4 ventilateurs pendant la maintenance: un profil EXISTANT d'OpenLinkHub, qui porte le nom d'un mode (static = couleur fixe, réglable dans l'éditeur de profils de lumière); vide = pas de rappel lumineux")
	ledStateFile   = flag.String("led-state", "", "fichier des profils d'origine des ventilateurs allumés (remis au prochain démarrage si la surveillance s'arrête brutalement)")
)

type ledZone struct{ From, To, Channel int }

func parseZones(s string) ([]ledZone, error) {
	var out []ledZone
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		rng, ch, ok := strings.Cut(part, ":")
		if !ok {
			return nil, fmt.Errorf("zone %q: attendu « baie-baie:canal »", part)
		}
		lo, hi, dash := strings.Cut(rng, "-")
		from, err1 := strconv.Atoi(strings.TrimSpace(lo))
		to, err2 := from, error(nil)
		if dash {
			to, err2 = strconv.Atoi(strings.TrimSpace(hi))
		}
		c, err3 := strconv.Atoi(strings.TrimSpace(ch))
		if err1 != nil || err2 != nil || err3 != nil || from < 1 || to < from {
			return nil, fmt.Errorf("zone %q invalide", part)
		}
		out = append(out, ledZone{from, to, c})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("aucune zone")
	}
	return out, nil
}

func zoneChannel(zones []ledZone, bay int) (int, bool) {
	for _, z := range zones {
		if bay >= z.From && bay <= z.To {
			return z.Channel, true
		}
	}
	return 0, false
}

// ledPlan dit quels ventilateurs doivent être allumés en continu, et si l'alternance est demandée.
type ledPlan struct {
	Steady    map[int]bool
	Alternate bool
}

// ledPlanOf déduit le plan lumineux des anomalies en cours. Fonction pure.
func ledPlanOf(fs []finding, zones []ledZone, extraction, all []int) ledPlan {
	p := ledPlan{Steady: map[int]bool{}}
	for _, f := range fs {
		crit := f.Level == levelCrit
		switch {
		case crit && (f.Key == "corsair-all" || f.Key == "corsair-extraction"):
			for _, ch := range all {
				p.Steady[ch] = true
			}
		case strings.HasPrefix(f.Key, "corsair-fan-"):
			if ch, err := strconv.Atoi(strings.TrimPrefix(f.Key, "corsair-fan-")); err == nil {
				p.Steady[ch] = true
			}
		case crit && strings.HasPrefix(f.Key, "disk-temp-"):
			bay, err := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(f.Key, "disk-temp-"), "sata"))
			if ch, ok := zoneChannel(zones, bay); err == nil && ok {
				p.Steady[ch] = true
			} else {
				// Baie inconnue: on ne sait pas quel ventilateur est le plus proche, tous les extracteurs.
				for _, c := range extraction {
					p.Steady[c] = true
				}
			}
		case crit && (f.Key == "cpu-temp" || f.Key == "board-temp" ||
			(strings.HasPrefix(f.Key, "probe-") && !strings.HasSuffix(f.Key, "-missing"))):
			p.Alternate = true
		}
	}
	return p
}

type ledEngine struct {
	profile, normal string
	order           []int // extracteurs de gauche à droite
	zones           []ledZone
	extraction, all []int
	alt             time.Duration
	statePath       string

	maintProfile string // profil de lumière pendant la maintenance (vide = pas de rappel lumineux)

	orig         map[int]string // canaux allumés -> profil d'origine à remettre
	cur          map[int]string // canaux allumés -> profil actuellement appliqué (alerte ou maintenance)
	hub          string
	lastErr      string
	backoffUntil time.Time // après un échec, on laisse OpenLinkHub tranquille quelques secondes

	fetch func() (hubFans, error)
	set   func(hubID string, channel int, profile string) error
}

func (e *ledEngine) report(err error) {
	if err == nil {
		e.lastErr = ""
		return
	}
	if e.lastErr != err.Error() {
		e.lastErr = err.Error()
		log.Printf("ATTENTION: alerte lumineuse impossible: %v", err)
	}
}

// tick amène les LED dans l'état voulu par les anomalies en cours. Sans anomalie et sans ventilateur
// allumé, il ne fait strictement rien (aucun appel à OpenLinkHub).
func (e *ledEngine) tick(fs []finding, now time.Time) { e.tickM(fs, false, now) }

// tickM est tick avec l'état de la maintenance: pendant la maintenance, les ventilateurs qui ne sont pas
// en alerte prennent le profil -led-maintenance (rappel visible que la protection est suspendue); une
// alerte l'emporte toujours sur ce rappel.
func (e *ledEngine) tickM(fs []finding, maint bool, now time.Time) {
	if e.cur == nil {
		e.cur = map[int]string{}
	}
	if now.Before(e.backoffUntil) {
		return
	}
	plan := ledPlanOf(fs, e.zones, e.extraction, e.all)
	want := map[int]string{}
	for ch := range plan.Steady {
		want[ch] = e.profile
	}
	if plan.Alternate && len(e.order) > 0 && e.alt > 0 {
		want[e.order[int(now.UnixNano()/int64(e.alt))%len(e.order)]] = e.profile
	}
	if maint && e.maintProfile != "" {
		for _, ch := range e.all {
			if _, alerting := want[ch]; !alerting {
				want[ch] = e.maintProfile
			}
		}
	}
	if len(want) == 0 && len(e.orig) == 0 {
		return
	}
	changed, failed := false, false
	for _, ch := range sortedInts(e.orig) {
		w, keep := want[ch]
		if !keep {
			if err := e.set(e.hub, ch, e.orig[ch]); err != nil {
				e.report(err)
				failed = true
				continue
			}
			log.Printf("LED: ventilateur canal %d remis en lumière normale (%s)", ch, e.orig[ch])
			delete(e.orig, ch)
			delete(e.cur, ch)
			changed = true
			continue
		}
		if e.cur[ch] != w {
			if err := e.set(e.hub, ch, w); err != nil {
				e.report(err)
				failed = true
				continue
			}
			e.cur[ch] = w
			log.Printf("LED: ventilateur canal %d passe en %s", ch, w)
		}
	}
	var missing []int
	for ch := range want {
		if _, lit := e.orig[ch]; !lit {
			missing = append(missing, ch)
		}
	}
	sort.Ints(missing)
	if len(missing) > 0 {
		hf, err := e.fetch()
		if err != nil {
			e.report(err)
			failed = true
		} else {
			e.hub = hf.HubID
			current := map[int]string{}
			for _, f := range hf.Fans {
				current[f.Channel] = f.Profile
			}
			for _, ch := range missing {
				p, known := current[ch]
				if !known {
					continue
				}
				if p == "" || p == e.profile || (e.maintProfile != "" && p == e.maintProfile) {
					p = e.normal
				}
				if err := e.set(hf.HubID, ch, want[ch]); err != nil {
					e.report(err)
					failed = true
					continue
				}
				e.orig[ch] = p
				e.cur[ch] = want[ch]
				changed = true
				log.Printf("LED: ventilateur canal %d en %s", ch, want[ch])
			}
		}
	}
	if failed {
		e.backoffUntil = now.Add(20 * time.Second)
	}
	if changed {
		if !failed {
			e.report(nil)
		}
		e.saveState()
	}
}

// restoreAll remet tous les ventilateurs allumés en lumière normale (arrêt de la surveillance).
func (e *ledEngine) restoreAll() {
	for _, ch := range sortedInts(e.orig) {
		if err := e.set(e.hub, ch, e.orig[ch]); err != nil {
			e.report(err)
			continue
		}
		delete(e.orig, ch)
		delete(e.cur, ch)
	}
	e.saveState()
}

func sortedInts(m map[int]string) []int {
	var out []int
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

type ledState struct {
	Canaux map[string]string `json:"canaux"`
}

func (e *ledEngine) saveState() {
	if e.statePath == "" {
		return
	}
	if len(e.orig) == 0 {
		os.Remove(e.statePath)
		return
	}
	st := ledState{Canaux: map[string]string{}}
	for ch, p := range e.orig {
		st.Canaux[strconv.Itoa(ch)] = p
	}
	b, _ := json.Marshal(st)
	tmp := e.statePath + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, e.statePath)
	}
}

// recover remet les ventilateurs que la surveillance précédente avait allumés avant de s'arrêter.
func (e *ledEngine) recover() {
	if e.statePath == "" {
		return
	}
	raw, err := os.ReadFile(e.statePath)
	if err != nil {
		return
	}
	var st ledState
	if json.Unmarshal(raw, &st) != nil || len(st.Canaux) == 0 {
		os.Remove(e.statePath)
		return
	}
	hf, err := e.fetch()
	if err != nil {
		e.report(err)
		return // on réessaiera au prochain démarrage: le fichier est gardé
	}
	for k, p := range st.Canaux {
		ch, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		if err := e.set(hf.HubID, ch, p); err != nil {
			e.report(err)
			continue
		}
		log.Printf("LED: ventilateur canal %d remis en lumière normale (%s) après l'arrêt de la surveillance précédente", ch, p)
	}
	os.Remove(e.statePath)
}

// startLEDs branche le moteur: reprise après arrêt brutal, boucle d'une seconde, remise en état à l'arrêt.
func startLEDs(st *shared) {
	zones, err := parseZones(*ledZonesFlag)
	if err != nil {
		log.Fatalf("-led-zones: %v", err)
	}
	order, err := parseChannels(*ledOrder)
	if err != nil {
		log.Fatalf("-led-order: %v", err)
	}
	base := *baseURL
	all := append(append([]int(nil), lim.ExtractionFans...), lim.IntakeFan)
	e := &ledEngine{profile: *ledProfile, normal: *ledNormal, order: order, zones: zones,
		extraction: append([]int(nil), lim.ExtractionFans...), all: all, alt: *ledAlternate, statePath: *ledStateFile,
		maintProfile: *ledMaintenance, orig: map[int]string{}, cur: map[int]string{},
		fetch: func() (hubFans, error) { return fetchHubFans(base) },
		set:   func(hub string, ch int, p string) error { return setFanProfile(base, hub, ch, p) }}
	safely("reprise des LED", e.recover)
	log.Printf("Alerte lumineuse: profil %q, extracteurs de gauche à droite %v, alternance %s, zones %s", e.profile, order, e.alt, *ledZonesFlag)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt) // Ctrl+C lors d'un essai à la main
	go func() {
		<-sig
		e.restoreAll()
		os.Exit(0)
	}()
	go func() {
		for {
			safely("alerte lumineuse", func() {
				now := time.Now()
				_, fs := st.view(now)
				_, maint := maintenanceUntil(*maintFile, now)
				e.tickM(fs, maint, now)
			})
			time.Sleep(time.Second)
		}
	}()
}

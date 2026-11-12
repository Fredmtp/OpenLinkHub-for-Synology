package main

import (
	"fmt"
	"sync"
	"time"
)

// Exécution des ordres décidés par hueGuard (voir hueguard.go): commande de la prise, relecture de son état,
// relance d'OpenLinkHub après un cycle, notification et journal des événements.

var hueSleep = time.Sleep // remplacé dans les essais

// hueRun exécute un ordre et renvoie l'état relu de la prise.
//
//	"on":    allume la prise.
//	"cycle": l'éteint 15 s puis la rallume (jusqu'à 5 essais pour la rallumer: elle ne doit jamais rester éteinte).
func hueRun(p plugDevice, action string) (plugState, error) {
	switch action {
	case "on":
		if err := p.Set(true); err != nil {
			return plugState{}, err
		}
	case "cycle":
		if err := p.Set(false); err != nil {
			return plugState{}, fmt.Errorf("coupure refusée par le Bridge: %v", err)
		}
		hueSleep(15 * time.Second)
		var err error
		for i := 0; i < 5; i++ {
			if err = p.Set(true); err == nil {
				break
			}
			hueSleep(3 * time.Second)
		}
		if err != nil {
			return plugState{}, fmt.Errorf("prise coupée mais impossible de la rallumer: %v", err)
		}
	default:
		return plugState{}, fmt.Errorf("ordre inconnu: %q", action)
	}
	hueSleep(4 * time.Second) // laisse le Bridge enregistrer le changement
	return readPlug(p, plugState{}, time.Now()), nil
}

func hueMessage(action, why string, st plugState, err error, dry bool, page string) message {
	what := "rallumée"
	if action == "cycle" {
		what = "coupée 15 secondes puis rallumée"
	}
	var res string
	prio := 5
	tags := []string{"warning"}
	switch {
	case dry:
		res = "mode à blanc: aucun ordre envoyé."
		prio = 3
	case err != nil:
		res = "ÉCHEC de l'ordre: " + err.Error() + "."
		tags = []string{"rotating_light"}
	case st.Known && st.On:
		res = "Le Bridge confirme que la prise est allumée."
	default:
		res = "Le Bridge ne confirme pas l'état de la prise (relecture impossible ou prise encore éteinte)."
		tags = []string{"rotating_light"}
	}
	body := fmt.Sprintf("Prise Hue « Aération NAS » %s par la surveillance.\nCause: %s.\n%s\n", what, why, res)
	if !dry && err == nil {
		body += "OpenLinkHub est relancé ensuite pour retrouver le Hub (il ne relit pas un Hub qui a perdu son courant).\n"
	}
	if page != "" {
		body += "\nPage d'état: " + page + "\n"
	}
	return message{
		Subject:  "[NAS meuble] prise des ventilateurs " + map[bool]string{true: "coupée puis rallumée", false: "rallumée"}[action == "cycle"],
		Body:     body,
		Short:    fmt.Sprintf("Prise des ventilateurs %s: %s. %s", what, why, res),
		Priority: prio, Tags: tags,
	}
}

// hueCtl relie la décision (hueGuard), la prise, les notifications et l'état partagé.
type hueCtl struct {
	plug       plugDevice
	guard      *hueGuard
	st         *shared
	post       func(message)
	events     *eventLog
	dry        bool
	afterCycle func() error // relance d'OpenLinkHub seul (start.sh hub); nil = aucune
	page       string

	mu     sync.Mutex // une seule exécution à la fois
	prev   plugState
	logger func(format string, a ...any)
}

func newHueCtl(p plugDevice, g *hueGuard, st *shared, post func(message), ev *eventLog, dry bool, after func() error, page string) *hueCtl {
	return &hueCtl{plug: p, guard: g, st: st, post: post, events: ev, dry: dry, afterCycle: after, page: page, logger: logf}
}

// poll relit la prise régulièrement (goroutine de la collecte lente: un Bridge qui tarde ne retarde pas la sécurité).
func (c *hueCtl) poll(every time.Duration) {
	for {
		safely("lecture de la prise Hue", func() { c.refresh() })
		time.Sleep(every)
	}
}

func (c *hueCtl) refresh() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readLocked()
}

func (c *hueCtl) readLocked() {
	now := time.Now()
	next := readPlug(c.plug, c.prev, now)
	if next.ErrSince.IsZero() && !c.prev.ErrSince.IsZero() {
		c.logger("Bridge Hue de nouveau joignable (prise %s)", map[bool]string{true: "allumée", false: "éteinte"}[next.On])
	} else if !next.ErrSince.IsZero() && c.prev.ErrSince.IsZero() {
		c.logger("ATTENTION: Bridge Hue injoignable (%s)", next.LastErr)
	}
	if next.Known && c.prev.Known && next.On != c.prev.On {
		c.logger("Prise Hue %s", map[bool]string{true: "allumée", false: "éteinte"}[next.On])
	}
	c.prev = next
	c.st.setHue(true, next)
}

// step applique les règles à l'instant now. Renvoie une raison d'arrêt du NAS (vide = pas d'arrêt).
// L'exécution de l'ordre se fait en arrière-plan: la boucle de sécurité n'attend jamais le Bridge.
func (c *hueCtl) step(in assessInput, now time.Time, vent ventState, ventLostSince time.Time) string {
	diskMax, diskOK := 0.0, false
	if in.HaveNAS {
		if _, t, ok := in.NAS.maxDiskTemp(); ok {
			diskMax, diskOK = t, true
		}
	}
	d := c.guard.decide(hueIn{
		Now: now, State: in.Hue.State, Enforce: in.Enforce, Maintenance: in.Maintenance, DevsOK: in.DevsOK, Devs: in.Devs,
		Vent: vent, VentLostSince: ventLostSince, ProbeStill: in.ProbeStill, FanStill: in.FanStill, DiskMax: diskMax, DiskMaxOK: diskOK,
		NasCh: in.NasCh, QnapCh: in.QnapCh,
	})
	if d.Action != "" {
		c.st.setHueOrder(now, d.Action)
		go safely("ordre à la prise Hue", func() { c.exec(d) })
	}
	return d.Shutdown
}

func (c *hueCtl) exec(d hueDecision) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logger("PRISE HUE: ordre « %s » (%s)", d.Action, d.Why)
	var st plugState
	var err error
	if c.dry {
		c.logger("(mode à blanc: aucun ordre envoyé à la prise)")
	} else {
		st, err = hueRun(c.plug, d.Action)
		if err != nil {
			c.logger("ECHEC de l'ordre à la prise Hue: %v", err)
		} else {
			c.prev = st
			c.st.setHue(true, st)
		}
	}
	msg := hueMessage(d.Action, d.Why, st, err, c.dry, c.page)
	if c.post != nil {
		c.post(msg)
	}
	if c.events != nil {
		c.events.add(eventRec{At: time.Now(), Kind: "event", Key: "hue", Title: msg.Subject, Text: d.Why, Gravity: "haute"})
	}
	// Après TOUT ordre réussi (rallumage ou cycle), le Hub a perdu puis retrouvé son courant: OpenLinkHub ne le relit pas
	// tout seul (il garde des valeurs figées, nuit du 12/11) et doit être relancé avec le reset USB du Hub.
	if !c.dry && err == nil && c.afterCycle != nil {
		hueSleep(20 * time.Second) // le Hub redémarre avec la prise: on laisse un peu de temps avant de relancer OpenLinkHub
		if e := c.afterCycle(); e != nil {
			c.logger("ATTENTION: relance d'OpenLinkHub impossible après l'ordre à la prise: %v", e)
		} else {
			c.logger("OpenLinkHub relancé après l'ordre à la prise")
		}
	}
}

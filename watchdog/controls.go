package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Page de commandes (décision du 28/10/2026): quelques boutons pour ne pas avoir à ouvrir un terminal.
// Elle agit comme les scripts maintenance.sh et silence.sh, avec les mêmes plafonds, et reste PROTÉGÉE:
//   - mot de passe obligatoire (CONTROL_PASSWORD dans secrets.env, 8 caractères au moins); sans lui, la
//     page est désactivée. HTTP Basic: le mot de passe circule en clair sur le réseau local;
//   - jeton aléatoire dans chaque formulaire (une page d'un autre site ne peut pas déclencher d'action);
//   - blocage de 10 minutes après 10 échecs de mot de passe en 10 minutes.
// Actions: maintenance 30 min / fin, silence sonore 30 min / fin, test des bips, relance de la
// surveillance (option -restart-cmd).

var restartCmd = flag.String("restart-cmd", "", "commande qui relance la surveillance seule (ex. « sh /volume1/openlinkhub/start.sh surveillance »), lancée détachée; vide = bouton absent")

const (
	controlMinutes  = 30
	controlLockN    = 10
	controlLockSpan = 10 * time.Minute
)

type controlCtx struct {
	password string
	csrf     string
	beep     func()
	cfg      alarmCfg
	restart  func() error
	ev       *eventLog
	dry      bool

	mu       sync.Mutex
	fails    []time.Time
	lastTest time.Time
	delay    time.Duration // pause après un échec (1 s; 0 dans les tests)
}

func newControlCtx(password string, beep func(), cfg alarmCfg, restart func() error, ev *eventLog, dry bool) *controlCtx {
	b := make([]byte, 16)
	rand.Read(b)
	return &controlCtx{password: password, csrf: hex.EncodeToString(b), beep: beep, cfg: cfg, restart: restart, ev: ev, dry: dry, delay: time.Second}
}

func (c *controlCtx) enabled() bool { return c != nil && len(c.password) >= 8 }

// locked dit si trop d'échecs récents bloquent l'authentification.
func (c *controlCtx) locked(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := c.fails[:0]
	for _, t := range c.fails {
		if now.Sub(t) < controlLockSpan {
			kept = append(kept, t)
		}
	}
	c.fails = kept
	return len(c.fails) >= controlLockN
}

func (c *controlCtx) fail(now time.Time) {
	c.mu.Lock()
	c.fails = append(c.fails, now)
	c.mu.Unlock()
}

// auth vérifie le mot de passe (Basic, n'importe quel nom d'utilisateur). Renvoie faux et a répondu si refusé.
func (c *controlCtx) auth(rw http.ResponseWriter, r *http.Request, now time.Time) bool {
	if !c.enabled() {
		http.Error(rw, "Commandes désactivées: ajoutez CONTROL_PASSWORD (8 caractères au moins) dans secrets.env.", http.StatusNotFound)
		return false
	}
	if c.locked(now) {
		http.Error(rw, "Trop d'échecs: authentification bloquée pendant 10 minutes.", http.StatusTooManyRequests)
		return false
	}
	_, pass, ok := r.BasicAuth()
	if ok && subtle.ConstantTimeCompare([]byte(pass), []byte(c.password)) == 1 {
		return true
	}
	if ok {
		c.fail(now)
		log.Printf("ATTENTION: mot de passe refusé sur la page de commandes (depuis %s)", r.RemoteAddr)
		time.Sleep(c.delay)
	}
	rw.Header().Set("WWW-Authenticate", `Basic realm="Commandes du meuble", charset="UTF-8"`)
	http.Error(rw, "Authentification requise.", http.StatusUnauthorized)
	return false
}

// writeUntil écrit une date de fin (secondes Unix) comme maintenance.sh et silence.sh.
func writeUntil(path string, until time.Time) error {
	if path == "" {
		return fmt.Errorf("fonction non configurée sur cette surveillance")
	}
	return os.WriteFile(path, []byte(strconv.FormatInt(until.Unix(), 10)+"\n"), 0o644)
}

func removeFile(path string) error {
	if path == "" {
		return fmt.Errorf("fonction non configurée sur cette surveillance")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// playSeries joue une série de bips d'alerte (essai du buzzer).
func (c *controlCtx) playSeries() {
	for i := 0; i < c.cfg.Count; i++ {
		c.beep()
		if i < c.cfg.Count-1 {
			time.Sleep(c.cfg.Gap)
		}
	}
}

// do exécute une action et renvoie le message à afficher.
func (c *controlCtx) do(action string, now time.Time) (string, error) {
	switch action {
	case "maintenance-on":
		if err := writeUntil(*maintFile, now.Add(controlMinutes*time.Minute)); err != nil {
			return "", err
		}
		return fmt.Sprintf("Maintenance activée jusqu'à %s (les seuils de température restent actifs).", now.Add(controlMinutes*time.Minute).Format("15:04")), nil
	case "maintenance-off":
		if err := removeFile(*maintFile); err != nil {
			return "", err
		}
		return "Maintenance terminée: l'arrêt ventilation est rétabli.", nil
	case "silence-on":
		if err := writeUntil(*silenceFile, now.Add(controlMinutes*time.Minute)); err != nil {
			return "", err
		}
		return fmt.Sprintf("Alerte sonore coupée jusqu'à %s (les notifications et les arrêts restent actifs).", now.Add(controlMinutes*time.Minute).Format("15:04")), nil
	case "silence-off":
		if err := removeFile(*silenceFile); err != nil {
			return "", err
		}
		return "Alerte sonore rétablie.", nil
	case "test-bips":
		if c.beep == nil {
			return "", fmt.Errorf("pas de buzzer configuré (option -buzzer)")
		}
		c.mu.Lock()
		recent := !c.lastTest.IsZero() && now.Sub(c.lastTest) < 10*time.Second
		if !recent {
			c.lastTest = now
		}
		c.mu.Unlock()
		if recent {
			return "", fmt.Errorf("un essai vient d'avoir lieu: patientez 10 secondes")
		}
		go c.playSeries()
		return "Série de bips lancée.", nil
	case "relancer":
		if c.restart == nil {
			return "", fmt.Errorf("relance non configurée (option -restart-cmd)")
		}
		if err := c.restart(); err != nil {
			return "", err
		}
		return "Relance de la surveillance demandée: la page répondra de nouveau dans quelques secondes.", nil
	}
	return "", fmt.Errorf("action inconnue")
}

// detachedRun lance une commande dans sa propre session, sans l'attendre: elle survit à la surveillance
// qu'elle relance (start.sh surveillance l'arrête, puis la redémarre).
func detachedRun(cmd string) error {
	c := exec.Command("sh", "-c", "sleep 1; "+cmd)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	go c.Wait()
	return nil
}

// ---- pages ----

func (c *controlCtx) handle(rw http.ResponseWriter, r *http.Request) {
	now := time.Now()
	if !c.auth(rw, r, now) {
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		c.render(rw, r, now)
	case http.MethodPost:
		r.Body = http.MaxBytesReader(rw, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			http.Error(rw, "requête illisible", http.StatusBadRequest)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(c.csrf)) != 1 {
			http.Error(rw, "Jeton invalide: rouvrez la page de commandes et recommencez.", http.StatusForbidden)
			return
		}
		action := r.PostForm.Get("action")
		msg, err := c.do(action, now)
		level := "ok"
		if err != nil {
			msg, level = err.Error(), "err"
			log.Printf("Commande « %s » refusée: %v (depuis %s)", action, err, r.RemoteAddr)
		} else {
			log.Printf("Commande « %s » exécutée depuis %s", action, r.RemoteAddr)
			if c.ev != nil {
				c.ev.add(eventRec{At: now, Kind: "event", Key: "commande", Title: "Commande depuis la page de commandes", Text: msg, Gravity: "basse"})
			}
		}
		http.Redirect(rw, r, "/commandes?"+url.Values{"m": {msg}, "n": {level}}.Encode(), http.StatusSeeOther)
	default:
		http.Error(rw, "méthode non autorisée", http.StatusMethodNotAllowed)
	}
}

func (c *controlCtx) render(rw http.ResponseWriter, r *http.Request, now time.Time) {
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	p(`<!doctype html><html lang="fr"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Commandes du meuble</title><style>
:root{--bg:#f6f7f9;--card:#fff;--fg:#1c2330;--mut:#667;--ok:#1b8a3a;--warn:#b26a00;--crit:#c0262d;--line:#dde1e7;--btn:#1f4fd8}
@media (prefers-color-scheme:dark){:root{--bg:#111418;--card:#1a1f26;--fg:#e6e9ee;--mut:#97a0ad;--ok:#4cc16b;--warn:#e6a23c;--crit:#ff6b6b;--line:#2b323c;--btn:#6d8cff}}
body{margin:0;padding:16px;background:var(--bg);color:var(--fg);font:16px/1.45 system-ui,sans-serif}
main{max-width:640px;margin:0 auto}h1{font-size:20px;margin:0 0 12px}h2{font-size:15px;margin:0 0 8px;color:var(--mut);font-weight:600}
.card{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:14px;margin-bottom:12px}
.msg{padding:12px 14px;border-radius:10px;margin-bottom:12px;border:1px solid var(--line);background:var(--card);font-weight:600}
.ok{color:var(--ok)}.err{color:var(--crit)}.warn{color:var(--warn);font-weight:600}.mut{color:var(--mut);font-size:13px}
form{display:inline-block;margin:4px 6px 4px 0}button{font:inherit;padding:10px 14px;border-radius:8px;border:1px solid var(--btn);background:var(--btn);color:#fff;cursor:pointer}
button.off{background:transparent;color:var(--btn)}label{font-size:14px;margin-right:6px}a{color:var(--btn)}</style></head><body><main>
<h1>Commandes du meuble</h1><p class="mut"><a href="/">← Retour à l'état</a></p>`)
	if m := r.URL.Query().Get("m"); m != "" {
		cl := "ok"
		if r.URL.Query().Get("n") == "err" {
			cl = "err"
		}
		p(`<div class="msg %s">%s</div>`, cl, esc(m))
	}

	// état courant
	p(`<div class="card"><h2>État</h2>`)
	if until, on := maintenanceUntil(*maintFile, now); on {
		p(`<p class="warn">Maintenance ACTIVE jusqu'à %s: l'arrêt lié aux ventilateurs est suspendu.</p>`, until.Format("15:04"))
	} else {
		p(`<p class="ok">Maintenance inactive: la protection est complète.</p>`)
	}
	if until, on := maintenanceUntil(*silenceFile, now); on {
		p(`<p class="warn">Alerte sonore coupée jusqu'à %s.</p>`, until.Format("15:04"))
	} else {
		p(`<p class="ok">Alerte sonore active.</p>`)
	}
	mode := "arrêt automatique ACTIF"
	if c.dry {
		mode = "mode à blanc (n'éteint rien)"
	}
	p(`<p class="mut">Surveillance: %s.</p></div>`, esc(mode))

	btn := func(action, label, class string) string {
		return fmt.Sprintf(`<form method="post" action="/commandes"><input type="hidden" name="csrf" value="%s"><input type="hidden" name="action" value="%s"><button class="%s" type="submit">%s</button></form>`,
			c.csrf, action, class, esc(label))
	}
	p(`<div class="card"><h2>Maintenance (NAS allumé, intervention sur le meuble)</h2>%s%s
<p class="mut">Suspend seulement l'arrêt lié aux ventilateurs, %d minutes (4 h au plus). Les seuils de température restent actifs.</p></div>`,
		btn("maintenance-on", fmt.Sprintf("Maintenance %d min", controlMinutes), ""), btn("maintenance-off", "Fin de maintenance", "off"), controlMinutes)
	p(`<div class="card"><h2>Alerte sonore</h2>%s%s%s
<p class="mut">Le silence coupe seulement les bips; les notifications et les arrêts automatiques restent actifs.</p></div>`,
		btn("silence-on", fmt.Sprintf("Silence %d min", controlMinutes), ""), btn("silence-off", "Rétablir le son", "off"), btn("test-bips", "Tester les bips", "off"))
	if c.restart != nil {
		p(`<div class="card"><h2>Surveillance</h2><form method="post" action="/commandes"><input type="hidden" name="csrf" value="%s"><input type="hidden" name="action" value="relancer">
<label><input type="checkbox" required> Je confirme</label><button class="off" type="submit">Relancer la surveillance</button></form>
<p class="mut">Relance la surveillance seule (pas OpenLinkHub ni le Hub). Elle est aveugle quelques secondes.</p></div>`, c.csrf)
	}
	p(`<p class="mut">Page protégée par mot de passe, réseau local uniquement. Toutes les commandes sont inscrites au journal.</p></main></body></html>`)
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Write([]byte(b.String()))
}

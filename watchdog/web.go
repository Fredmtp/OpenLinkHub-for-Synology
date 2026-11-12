package main

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
)

// Page d'état en lecture seule (aucune action possible). Elle n'écoute que sur l'adresse du réseau
// local passée à -listen et n'a pas d'authentification: ne l'exposez pas hors du réseau local.

type webCtx struct {
	st      *shared
	al      *alerter
	hist    *history
	nt      *notifier
	ev      *eventLog
	ai      aiAnalyzer
	started time.Time
	dry     bool
	nasCh   int
	qnapCh  int
	ctl     *controlCtx // page de commandes (nil ou désactivée sans CONTROL_PASSWORD)
	recDir  string      // dossier des relevés CSV (-record), relu par /api/releves pour les graphiques
}

func (w *webCtx) serve(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", w.page)
	mux.HandleFunc("/commandes", func(rw http.ResponseWriter, r *http.Request) { w.ctl.handle(rw, r) })
	mux.HandleFunc("/etat.json", w.jsonState)
	mux.HandleFunc("/api/tuiles", w.tilesJSON)
	mux.HandleFunc("/api/releves", w.releves)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		logf("ATTENTION: page d'état indisponible (%v)", err)
	}
}

func (w *webCtx) jsonState(rw http.ResponseWriter, r *http.Request) {
	in, fs := w.st.view(time.Now())
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(rw).Encode(map[string]any{
		"mesure_nas": in.NAS, "meuble": in.Devs, "api_ok": in.DevsOK, "anomalies": fs,
		"mode_a_blanc": w.dry, "demarre": w.started,
	})
}

// tilesJSON alimente les tuiles « NAS » du tableau de bord d'OpenLinkHub (autre port: d'où l'en-tête CORS).
// Lecture seule et données non sensibles (températures, états): l'ouverture à toute origine du réseau local suffit.
func (w *webCtx) tilesJSON(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(rw, "méthode non autorisée", http.StatusMethodNotAllowed)
		return
	}
	in, fs := w.st.view(time.Now())
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.Header().Set("Access-Control-Allow-Origin", "*")
	rw.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(rw).Encode(map[string]any{"tiles": buildTiles(in, fs, lim)})
}

func esc(s string) string { return html.EscapeString(s) }

func cls(level string) string {
	switch level {
	case levelCrit:
		return "crit"
	case levelWarn:
		return "warn"
	}
	return "ok"
}

func tempClass(t, warn, crit float64) string {
	switch {
	case t >= crit:
		return "crit"
	case t >= warn:
		return "warn"
	}
	return "ok"
}

func (w *webCtx) page(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(rw, r)
		return
	}
	now := time.Now()
	in, fs := w.st.view(now)
	lim := defaultLimits()
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }

	p(`<!doctype html><html lang="fr"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="30"><title>NAS meuble</title><style>
:root{--bg:#f6f7f9;--card:#fff;--fg:#1c2330;--mut:#667;--ok:#1b8a3a;--warn:#b26a00;--crit:#c0262d;--line:#dde1e7}
@media (prefers-color-scheme:dark){:root{--bg:#111418;--card:#1a1f26;--fg:#e6e9ee;--mut:#97a0ad;--ok:#4cc16b;--warn:#e6a23c;--crit:#ff6b6b;--line:#2b323c}}
body{margin:0;padding:16px;background:var(--bg);color:var(--fg);font:15px/1.45 system-ui,sans-serif}
main{max-width:980px;margin:0 auto}h1{font-size:20px;margin:0 0 12px}h2{font-size:15px;margin:0 0 8px;color:var(--mut);font-weight:600}
.card{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:14px;margin-bottom:12px}
table{width:100%%;border-collapse:collapse}td,th{padding:5px 8px;border-bottom:1px solid var(--line);text-align:left}th{color:var(--mut);font-weight:500}
td.n{text-align:right;font-variant-numeric:tabular-nums}.ok{color:var(--ok)}.warn{color:var(--warn);font-weight:600}.crit{color:var(--crit);font-weight:700}
.banner{padding:12px 14px;border-radius:10px;font-weight:600;margin-bottom:12px;border:1px solid var(--line);background:var(--card)}
.mut{color:var(--mut);font-size:13px}</style></head><body><main><h1>NAS du meuble</h1>`)
	if w.ctl.enabled() {
		p(`<p class="mut"><a href="/commandes">Commandes (maintenance, silence, essai des bips)</a></p>`)
	}

	if until, on := maintenanceUntil(*maintFile, now); on {
		p(`<div class="banner warn">Maintenance: arrêt ventilation en veille jusqu'à %s (les seuils de température restent actifs)</div>`, until.Format("15:04"))
	}

	// Bandeau d'état
	switch {
	case len(fs) == 0:
		p(`<div class="banner ok">Tout est normal.</div>`)
	default:
		worst := levelWarn
		for _, f := range fs {
			if f.Level == levelCrit {
				worst = levelCrit
			}
		}
		p(`<div class="banner %s">%s en cours</div>`, cls(worst), plural(len(fs), "anomalie", "anomalies"))
		p(`<div class="card"><h2>Anomalies</h2><table>`)
		for _, f := range fs {
			p(`<tr><td class="%s">%s</td><td>%s</td></tr>`, cls(f.Level), esc(strings.ToUpper(f.Level)), esc(f.Text))
		}
		p(`</table></div>`)
	}

	// Meuble
	p(`<div class="card"><h2>Meuble (Corsair)</h2>`)
	if !in.DevsOK {
		p(`<p class="warn">OpenLinkHub injoignable</p>`)
	} else {
		p(`<table><tr><th>Élément</th><th class="n">Valeur</th></tr>`)
		for _, d := range in.Devs {
			switch {
			case d.IsTemperatureProbe:
				label := fmt.Sprintf("Sonde canal %d", d.ChannelID)
				if d.ChannelID == w.nasCh {
					label += " (NAS)"
				} else if d.ChannelID == w.qnapCh {
					label += " (QNAP)"
				}
				p(`<tr><td>%s</td><td class="n %s">%.1f °C</td></tr>`, esc(label), tempClass(d.Temperature, lim.ProbeWarn, lim.ProbeCrit), d.Temperature)
			case isFan(d):
				c := "ok"
				if d.RPM < lim.MinRPM {
					c = "crit"
				}
				p(`<tr><td>Ventilateur canal %d</td><td class="n %s">%d tr/min</td></tr>`, d.ChannelID, c, d.RPM)
			}
		}
		p(`</table>`)
	}
	p(`</div>`)

	if in.HaveNAS {
		s := in.NAS
		p(`<div class="card"><h2>NAS</h2><table><tr><th>Élément</th><th class="n">Valeur</th></tr>`)
		if s.CPUOK {
			p(`<tr><td>Processeur</td><td class="n %s">%.1f °C</td></tr>`, tempClass(s.CPU, lim.CPUWarn, lim.CPUCrit), s.CPU)
		}
		for _, k := range sortedKeys(s.BoardTemp) {
			t := s.BoardTemp[k]
			p(`<tr><td>Carte mère %s</td><td class="n %s">%.1f °C</td></tr>`, esc(k), tempClass(t, lim.BoardWarn, lim.BoardShutdown), t)
		}
		for _, n := range lim.NASFans {
			if rpm, ok := s.NASFans[n]; ok {
				c := "ok"
				if rpm < lim.MinRPM {
					c = "crit"
				}
				p(`<tr><td>Ventilateur interne n°%d</td><td class="n %s">%d tr/min</td></tr>`, n, c, rpm)
			}
		}
		if s.VolOK {
			p(`<tr><td>Volume 1</td><td class="n %s">%.0f %% utilisé</td></tr>`, tempClass(s.VolUsed, lim.VolWarn, lim.VolCrit), s.VolUsed)
		}
		for _, r := range s.RAID {
			if r.Total >= 4 || r.Level == "raid1" && r.Name == "md0" {
				c := "ok"
				if r.Status != "ok" {
					c = "crit"
				}
				p(`<tr><td>RAID %s (%s)</td><td class="n %s">%d/%d · %s</td></tr>`, esc(r.Name), esc(r.Level), c, r.Up, r.Total, esc(r.Status))
			}
		}
		if s.UPS.Present {
			c := "ok"
			if strings.Contains(s.UPS.Status, "OB") || strings.Contains(s.UPS.Status, "LB") {
				c = "crit"
			}
			p(`<tr><td>Onduleur</td><td class="n %s">%s · %.0f %% · %.0f min</td></tr>`, c, esc(s.UPS.Status), s.UPS.Charge, s.UPS.RuntimeS/60)
		}
		p(`<tr><td>Fonctionne depuis</td><td class="n">%s</td></tr></table></div>`, fmtDur(s.Uptime))

		p(`<div class="card"><h2>Disques</h2><table><tr><th>Disque</th><th class="n">Température</th><th>SMART</th><th class="n">Réalloués / en attente / erreurs</th></tr>`)
		for _, d := range s.Disks {
			if d.Standby {
				p(`<tr><td>%s</td><td colspan="3" class="mut">en veille</td></tr>`, esc(d.Name))
				continue
			}
			c := "ok"
			if d.TempOK {
				c = tempClass(d.Temp, lim.DiskWarn, lim.DiskCrit)
			}
			p(`<tr><td>%s</td><td class="n %s">%.0f °C</td><td>%s</td><td class="n">%d / %d / %d</td></tr>`,
				esc(d.Name), c, d.Temp, esc(orDash(d.Health)), d.Reallocated, d.Pending, d.Reported)
		}
		p(`</table></div>`)
	}

	// Historique
	if lines := w.hist.lines(); len(lines) > 0 {
		p(`<div class="card"><h2>Dernières 24 h</h2><table>`)
		for _, k := range metricOrder {
			s := w.hist.span(k)
			if !s.OK {
				continue
			}
			u := " °C"
			if strings.Contains(k, "tr/min") {
				u = ""
			}
			p(`<tr><td>%s</td><td class="n">actuel %.0f%s</td><td class="n">min %.0f</td><td class="n">max %.0f</td></tr>`, esc(k), s.Last, u, s.Min, s.Max)
		}
		p(`</table></div>`)
	}

	// Événements des 7 derniers jours (à confronter aux impressions 3D, lave-linge, etc.)
	if evs := w.ev.since(now.Add(-7 * 24 * time.Hour)); len(evs) > 0 {
		p(`<div class="card"><h2>Événements des 7 derniers jours</h2><table><tr><th>Quand</th><th>Quoi</th><th>Gravité</th></tr>`)
		for i := len(evs) - 1; i >= 0 && i >= len(evs)-30; i-- {
			e := evs[i]
			c := "ok"
			if e.Gravity == "critique" {
				c = "crit"
			} else if e.Gravity == "haute" {
				c = "warn"
			}
			p(`<tr><td>%s</td><td>%s</td><td class="%s">%s</td></tr>`, e.At.Format("02/01 15:04:05"), esc(e.Title+" — "+e.Text), c, esc(gravities[e.Gravity].Label))
		}
		p(`</table></div>`)
	}

	// Surveillance
	ok, errs := w.nt.status()
	mode := "ARRÊT AUTOMATIQUE ACTIF"
	mc := "ok"
	if w.dry {
		mode, mc = "mode à blanc (n'éteint rien)", "warn"
	}
	p(`<div class="card"><h2>Surveillance</h2><table><tr><td>Mode</td><td class="n %s">%s</td></tr>`, mc, esc(mode))
	p(`<tr><td>Démarrée</td><td class="n">%s</td></tr>`, w.started.Format("02/01 15:04"))
	if w.ai == nil {
		p(`<tr><td>Analyse IA</td><td class="n warn">désactivée (texte standard)</td></tr>`)
	} else {
		s := w.ai.stats()
		c := "ok"
		if s.LastError != "" {
			c = "warn"
		}
		p(`<tr><td>Analyse IA</td><td class="n %s">%s · %d appel(s) aujourd'hui sur %d · %d échec(s)</td></tr>`,
			c, esc(s.Model), s.CallsToday, s.MaxPerDay, s.Failures)
		if s.LastError != "" {
			p(`<tr><td>Dernière erreur IA</td><td class="n warn">%s</td></tr>`, esc(s.LastError))
		}
	}
	switch {
	case !w.nt.enabled():
		p(`<tr><td>Notifications</td><td class="n warn">désactivées</td></tr>`)
	case len(errs) > 0:
		for k, v := range errs {
			p(`<tr><td>Notification %s</td><td class="n crit">échec: %s</td></tr>`, esc(k), esc(v))
		}
	default:
		last := "aucune envoyée"
		if !ok.IsZero() {
			last = ok.Format("02/01 15:04")
		}
		p(`<tr><td>Notifications</td><td class="n ok">actives (dernier envoi: %s)</td></tr>`, esc(last))
	}
	p(`</table></div><p class="mut">Mis à jour à %s · actualisation automatique toutes les 30 s</p></main></body></html>`, now.Format("15:04:05"))

	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Write([]byte(b.String()))
}

func sortedKeys(m map[string]float64) []string {
	var k []string
	for key := range m {
		k = append(k, key)
	}
	for i := 1; i < len(k); i++ {
		for j := i; j > 0 && k[j] < k[j-1]; j-- {
			k[j], k[j-1] = k[j-1], k[j]
		}
	}
	return k
}

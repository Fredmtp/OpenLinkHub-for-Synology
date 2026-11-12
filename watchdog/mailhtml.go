package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Mise en page des mails: identité visuelle sobre.
// Tableaux et styles dans chaque balise (messageries, Outlook compris), aucune image ni police
// externe, tout texte échappé: un texte produit par l'IA ne peut ni injecter du HTML ni un lien.

const (
	mhFont  = "'Segoe UI',Helvetica,Arial,sans-serif"
	mhInk   = "#1A1F2B"
	mhSoft  = "#5B6474"
	mhLine  = "#E6E9EE"
	mhBg    = "#EEF0F3"
	mhNight = "#14213D"
	mhLink  = "#1D4E89"
	mhGreen = "#17603C"
)

type gravStyle struct{ Label, Band, Tint, Ink, Border string }

var gravities = map[string]gravStyle{
	"critique": {"Critique", "#8E1B2E", "#FBE9EC", "#6F1424", "#F0C4CC"},
	"haute":    {"Haute", "#B54708", "#FEF0E6", "#8A3505", "#F3C9AE"},
	"moyenne":  {"Moyenne", "#8A6100", "#FDF5DC", "#6B4B00", "#EBDDAF"},
	"basse":    {"Information", "#3D6787", "#EAF1F7", "#2C4C64", "#C9DAE8"},
}

var gravOrder = []string{"critique", "haute", "moyenne", "basse"}

func gravRank(g string) int {
	for i, k := range gravOrder {
		if k == g {
			return i
		}
	}
	return len(gravOrder)
}

// measure est une ligne « étiquette / valeur ». Level colore la valeur: "", ok, warn, crit.
type measure struct{ Label, Value, Level string }

// card est une alerte mise en page: une carte colorée selon sa gravité.
type card struct {
	Key      string
	Gravity  string // critique, haute, moyenne, basse
	Where    string // à droite du bandeau: « Matériel : disque 2 »
	Title    string
	Meta     string
	Summary  string
	Action   string
	Why      string
	Measures []measure
	Equip    string
	Analyzed bool // texte rédigé par l'IA
	Push     bool // part tout de suite quelle que soit sa gravité (ex.: retour à la normale d'une alerte critique)
}

type mailOpts struct {
	Dry      bool
	AI       bool
	PageURL  string
	Kicker   string
	Title    string
	Intro    string
	Hour     string // « 13 H » pour un résumé
	KickerGr string // gravité qui colore le sur-titre
}

var frDays = []string{"dimanche", "lundi", "mardi", "mercredi", "jeudi", "vendredi", "samedi"}

func frDate(t time.Time) string {
	return fmt.Sprintf("%s %02d/%02d/%d · %02d:%02d", frDays[t.Weekday()], t.Day(), int(t.Month()), t.Year(), t.Hour(), t.Minute())
}

// fr1 écrit un nombre avec une décimale et une virgule: 28,8.
func fr1(f float64) string { return strings.Replace(fmt.Sprintf("%.1f", f), ".", ",", 1) }

// frInt écrit un entier avec des espaces entre milliers: 69 592.
func frInt(n int) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	out := strings.Join(parts, " ")
	if neg {
		out = "-" + out
	}
	return out
}

// ---- briques ----

func mhPage(title, date, content string) string {
	return `<!doctype html><html lang="fr"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<meta name="color-scheme" content="light only"><meta name="supported-color-schemes" content="light">` +
		`<title>` + esc(title) + `</title></head>` +
		`<body style="margin:0;padding:0;background:` + mhBg + `;">` +
		`<div style="display:none;max-height:0;overflow:hidden;opacity:0;">` + esc(title) + `</div>` +
		`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="` + mhBg + `" style="background:` + mhBg + `;"><tr><td align="center" style="padding:24px 12px;">` +
		`<table role="presentation" width="640" cellpadding="0" cellspacing="0" border="0" bgcolor="#FFFFFF" style="width:100%;max-width:640px;background:#FFFFFF;border:1px solid #DDE1E7;border-collapse:separate;border-radius:12px;">` +
		`<tr><td bgcolor="` + mhNight + `" style="background:` + mhNight + `;padding:20px 28px;border-radius:11px 11px 0 0;">` +
		`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr>` +
		`<td style="font-family:` + mhFont + `;font-size:20px;font-weight:700;color:#FFFFFF;">NAS meuble</td>` +
		`<td align="right" style="font-family:` + mhFont + `;font-size:13px;color:#C9D2E3;">` + esc(date) + `</td></tr></table></td></tr>` +
		content + `</table></td></tr></table></body></html>`
}

func mhRow(content, margin string, bottom, topLine bool) string {
	lines := ""
	if bottom {
		lines += "border-bottom:1px solid " + mhLine + ";"
	}
	if topLine {
		lines += "border-top:1px solid " + mhLine + ";"
	}
	return `<tr><td style="padding:` + margin + `;` + lines + `font-family:` + mhFont + `;color:` + mhInk + `;">` + content + `</td></tr>`
}

// mhP écrit un paragraphe; raw=true laisse passer du HTML déjà construit par ce fichier.
func mhP(text string, size int, color, margin string, raw bool) string {
	if margin == "" {
		margin = "0 0 12px"
	}
	if !raw {
		text = esc(text)
	}
	return fmt.Sprintf(`<p style="margin:%s;font-family:%s;font-size:%dpx;line-height:1.55;color:%s;">%s</p>`, margin, mhFont, size, color, text)
}

func mhBox(label, text, gravity string) string {
	g := gravities[gravity]
	return `<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin:0 0 12px;"><tr>` +
		`<td bgcolor="` + g.Tint + `" style="background:` + g.Tint + `;padding:14px 16px;border-radius:8px;font-family:` + mhFont + `;">` +
		`<div style="margin:0 0 6px;font-size:12px;font-weight:700;letter-spacing:1px;color:` + g.Ink + `;">` + esc(label) + `</div>` +
		`<div style="font-size:15px;line-height:1.5;color:` + mhInk + `;">` + esc(text) + `</div></td></tr></table>`
}

func mhPill(text, bg, color string) string {
	return `<span style="display:inline-block;padding:4px 12px;border-radius:999px;background:` + bg + `;color:` + color +
		`;font-size:13px;font-weight:600;line-height:20px;white-space:nowrap;">` + esc(text) + `</span>`
}

func mhPills(counts map[string]int, extra ...[3]string) string {
	var parts []string
	for _, k := range gravOrder {
		g := gravities[k]
		bg, color := "#F2F4F7", "#667085"
		if counts[k] > 0 {
			bg, color = g.Band, "#FFFFFF"
		}
		parts = append(parts, mhPill(fmt.Sprintf("%s · %d", g.Label, counts[k]), bg, color))
	}
	for _, x := range extra {
		parts = append(parts, mhPill(x[0], x[1], x[2]))
	}
	return strings.Join(parts, " ")
}

func mhSection(title, body string) string {
	head := ""
	if title != "" {
		head = `<h2 style="margin:0 0 10px;font-size:18px;font-weight:700;color:` + mhInk + `;">` + esc(title) + `</h2>`
	}
	return mhRow(head+body, "24px 28px 8px", false, false)
}

func mhHeader(kicker, kickerColor, title, intro, pills string) string {
	h := `<div style="font-size:13px;font-weight:700;letter-spacing:1px;color:` + kickerColor + `;">` + esc(kicker) + `</div>` +
		`<h1 style="margin:10px 0 0;font-size:24px;line-height:1.3;font-weight:700;color:` + mhInk + `;">` + esc(title) + `</h1>`
	if intro != "" {
		h += mhP(intro, 15, mhSoft, "12px 0 0", false)
	}
	h += `<div style="margin-top:14px;line-height:2.2;">` + pills + `</div>`
	return mhRow(h, "26px 28px 22px", true, false)
}

func mhFooter(o mailOpts) string {
	ia := "."
	if o.AI {
		ia = " · analyse par l'IA (Claude). L'analyse peut se tromper : en cas de doute, fiez-vous aux mesures de la page d'état."
	}
	t := "Envoyé par la surveillance du NAS" + esc(ia)
	if o.PageURL != "" {
		t += `<br>Page d'état (réseau local) : <a href="` + esc(o.PageURL) + `" style="color:` + mhLink + `;">` + esc(o.PageURL) + `</a>`
	}
	return mhRow(`<div style="font-size:12px;line-height:1.6;color:`+mhSoft+`;">`+t+`</div>`, "18px 28px 24px", false, true)
}

var levelColors = map[string]string{"ok": mhGreen, "warn": "#8A6100", "crit": "#8E1B2E", "": mhInk}

func mhMeasures(ms []measure) string {
	var b strings.Builder
	for _, m := range ms {
		c, ok := levelColors[m.Level]
		if !ok {
			c = mhInk
		}
		b.WriteString(`<tr><td style="padding:6px 0;border-bottom:1px solid ` + mhLine + `;font-size:14px;color:` + mhSoft + `;">` + esc(m.Label) + `</td>` +
			`<td align="right" style="padding:6px 0;border-bottom:1px solid ` + mhLine + `;font-size:14px;font-weight:600;color:` + c + `;white-space:nowrap;">` + esc(m.Value) + `</td></tr>`)
	}
	return `<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin:0 0 12px;font-family:` + mhFont + `;">` + b.String() + `</table>`
}

func mhCard(c card, o mailOpts) string {
	g, ok := gravities[c.Gravity]
	if !ok {
		g = gravities["moyenne"]
		c.Gravity = "moyenne"
	}
	band := `<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr>` +
		`<td style="font-family:` + mhFont + `;font-size:13px;font-weight:700;letter-spacing:1px;color:#FFFFFF;">` + esc(strings.ToUpper(g.Label)) + `</td>` +
		`<td align="right" style="font-family:` + mhFont + `;font-size:13px;color:#FFFFFF;white-space:nowrap;">` + esc(c.Where) + `</td></tr></table>`
	var in []string
	in = append(in, `<div style="margin:0 0 6px;font-size:18px;line-height:1.35;font-weight:700;color:`+mhInk+`;">`+esc(c.Title)+`</div>`)
	if c.Meta != "" {
		in = append(in, `<div style="margin:0 0 12px;font-size:13px;color:`+mhSoft+`;">`+esc(c.Meta)+`</div>`)
	}
	if c.Summary != "" {
		in = append(in, mhP(c.Summary, 15, mhInk, "", false))
	}
	action := c.Action
	if action == "" {
		action = "Rien de particulier à faire."
	}
	in = append(in, mhBox("À FAIRE", action, c.Gravity))
	if len(c.Measures) > 0 {
		in = append(in, mhMeasures(c.Measures))
	}
	if c.Why != "" {
		in = append(in, mhP(`<strong style="color:`+mhInk+`;">Pourquoi :</strong> `+esc(c.Why), 13, mhSoft, "", true))
	}
	if !c.Analyzed && o.AI {
		in = append(in, mhP("Non analysé par l'IA : texte standard.", 12, mhSoft, "", false))
	}
	if c.Equip != "" {
		in = append(in, mhP(`<strong style="color:`+mhInk+`;">Équipement :</strong> `+esc(c.Equip), 13, mhSoft, "", true))
	}
	if o.PageURL != "" {
		in = append(in, `<div style="margin:0;font-size:13px;line-height:1.8;"><a href="`+esc(o.PageURL)+`" style="color:`+mhLink+`;">Page d'état du NAS</a></div>`)
	}
	return `<div style="margin:0 0 18px;"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="border-collapse:separate;border:1px solid ` + g.Border + `;border-radius:10px;">` +
		`<tr><td bgcolor="` + g.Band + `" style="background:` + g.Band + `;padding:10px 18px;border-radius:9px 9px 0 0;">` + band + `</td></tr>` +
		`<tr><td style="padding:18px;font-family:` + mhFont + `;color:` + mhInk + `;">` + strings.Join(in, "") + `</td></tr></table></div>`
}

func mhCards(cards []card, o mailOpts) string {
	sorted := append([]card(nil), cards...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if ri, rj := gravRank(sorted[i].Gravity), gravRank(sorted[j].Gravity); ri != rj {
			return ri < rj
		}
		return sorted[i].Key < sorted[j].Key
	})
	var b strings.Builder
	for _, c := range sorted {
		b.WriteString(mhCard(c, o))
	}
	return b.String()
}

// diskView est la tuile d'un disque dans le résumé.
type diskView struct{ Name, Temp, Level, Note string }

func mhDiskGrid(ds []diskView) string {
	colors := map[string][2]string{"ok": {"#F5F7FA", mhGreen}, "warn": {"#FDF5DC", "#8A6100"}, "crit": {"#FBE9EC", "#8E1B2E"}}
	var rows strings.Builder
	for i := 0; i < len(ds); i += 4 {
		end := i + 4
		if end > len(ds) {
			end = len(ds)
		}
		var cells strings.Builder
		for _, d := range ds[i:end] {
			c, ok := colors[d.Level]
			if !ok {
				c = colors["ok"]
			}
			cells.WriteString(`<td width="25%" style="padding:3px;"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr>` +
				`<td align="center" bgcolor="` + c[0] + `" style="background:` + c[0] + `;padding:10px 4px;border-radius:8px;font-family:` + mhFont + `;">` +
				`<div style="font-size:12px;color:` + mhSoft + `;">` + esc(d.Name) + `</div>` +
				`<div style="font-size:20px;font-weight:700;color:` + c[1] + `;">` + esc(d.Temp) + `</div>` +
				`<div style="font-size:11px;color:` + c[1] + `;">` + esc(d.Note) + `</div></td></tr></table></td>`)
		}
		rows.WriteString("<tr>" + cells.String() + "</tr>")
	}
	return `<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin:0 0 12px;">` + rows.String() + `</table>`
}

func countGravities(cards []card) map[string]int {
	m := map[string]int{}
	for _, c := range cards {
		m[c.Gravity]++
	}
	return m
}

func dryBox(dry bool) string {
	if !dry {
		return ""
	}
	return mhRow(mhBox("MODE À BLANC", "La surveillance journalise et vous prévient, mais elle n'éteint pas le NAS. Le passage en mode réel est prévu après la période d'observation.", "moyenne"), "20px 28px 0", false, false)
}

// renderAlertMail compose le mail d'une alerte immédiate (une ou plusieurs cartes).
func renderAlertMail(when time.Time, cards []card, o mailOpts) (subject, html string) {
	worst := "basse"
	for _, c := range cards {
		if gravRank(c.Gravity) < gravRank(worst) {
			worst = c.Gravity
		}
	}
	kicker := o.Kicker
	if kicker == "" {
		kicker = "À SAVOIR"
		if worst == "critique" || worst == "haute" {
			kicker = "ALERTE IMMÉDIATE"
		}
	}
	title := o.Title
	if title == "" {
		switch len(cards) {
		case 0:
			title = "Information"
		case 1:
			title = cards[0].Title
		default:
			title = fmt.Sprintf("%d points demandent votre attention", len(cards))
		}
	}
	body := mhHeader(kicker, gravities[worst].Band, title, o.Intro, mhPills(countGravities(cards))) +
		dryBox(o.Dry) + mhRow(mhCards(cards, o), "24px 28px 8px", false, false) + mhFooter(o)
	return "[NAS meuble] " + title, mhPage(title, frDate(when), body)
}

type knownItem struct{ Title, Note string }

// digestData regroupe ce que contient un résumé.
type digestData struct {
	Cards      []card // à savoir: événements des dernières heures
	Discarded  []card // écartés par l'IA, pour contrôle
	Known      []knownItem
	KnownShown bool       // la section « Déjà connus » n'est détaillée qu'une fois par semaine
	Events     []eventRec // événements des dernières 24 h, pour les confronter à ce qui se passait autour du NAS
	Meuble     []measure
	Disks      []diskView
	Equipment  []measure
}

func renderDigestMail(when time.Time, d digestData, o mailOpts) (subject, html string) {
	title := o.Title
	if title == "" {
		title = "Tout fonctionne"
		if n := len(d.Cards); n > 0 {
			title = fmt.Sprintf("%d point(s) à connaître", n)
		}
	}
	extra := [][3]string{}
	if n := len(d.Known); n > 0 {
		extra = append(extra, [3]string{fmt.Sprintf("Pris en compte · %d", n), "#F2F4F7", "#475467"})
	}
	if n := len(d.Discarded); n > 0 {
		extra = append(extra, [3]string{fmt.Sprintf("Écartés par l'IA · %d", n), "#F2F4F7", "#475467"})
	}
	worst := "basse"
	for _, c := range d.Cards {
		if gravRank(c.Gravity) < gravRank(worst) {
			worst = c.Gravity
		}
	}
	var b strings.Builder
	b.WriteString(mhHeader("RÉSUMÉ DE "+o.Hour, gravities[worst].Band, title, o.Intro, mhPills(countGravities(d.Cards), extra...)))
	b.WriteString(dryBox(o.Dry))
	if len(d.Cards) > 0 {
		b.WriteString(mhSection("À savoir", mhCards(d.Cards, o)))
	}
	if len(d.Events) > 0 {
		var ms []measure
		for _, e := range d.Events {
			when24 := e.At.Format("15:04")
			if e.At.YearDay() != when.YearDay() || e.At.Year() != when.Year() {
				when24 = e.At.Format("02/01 15:04")
			}
			lvl := ""
			if e.Gravity == "critique" || e.Gravity == "haute" {
				lvl = "warn"
			}
			ms = append(ms, measure{Label: when24 + " · " + e.Title, Value: gravities[e.Gravity].Label, Level: lvl})
		}
		note := mhP("Comparez ces heures à ce qui fonctionnait près du NAS (lave-linge, passage, portes) et aux tâches planifiées de DSM (sauvegarde, réplication).", 13, mhSoft, "", false)
		b.WriteString(mhSection("Événements des dernières 24 h", mhMeasures(ms)+note))
	}
	if len(d.Known) > 0 && d.KnownShown {
		var ms []measure
		for _, k := range d.Known {
			ms = append(ms, measure{Label: k.Title, Value: "Pris en compte", Level: "warn"})
		}
		note := mhP("Ces points ne sont plus rappelés chaque jour : un rappel par semaine, sauf aggravation.", 13, mhSoft, "", false)
		b.WriteString(mhSection("Déjà connus", mhMeasures(ms)+note))
	}
	if len(d.Discarded) > 0 {
		var ms []measure
		for _, c := range d.Discarded {
			ms = append(ms, measure{Label: c.Title, Value: "Écarté"})
		}
		b.WriteString(mhSection("Écartés par l'IA, pour contrôle", mhMeasures(ms)))
	}
	if len(d.Meuble) > 0 {
		b.WriteString(mhSection("Mesures des dernières 24 h", mhMeasures(d.Meuble)))
	}
	if len(d.Disks) > 0 {
		b.WriteString(mhSection("Disques", mhDiskGrid(d.Disks)))
	}
	if len(d.Equipment) > 0 {
		b.WriteString(mhSection("Équipement", mhMeasures(d.Equipment)))
	}
	b.WriteString(mhFooter(o))
	return "[NAS meuble] résumé de " + strings.ToLower(o.Hour) + " : " + title, mhPage(title, frDate(when), b.String())
}

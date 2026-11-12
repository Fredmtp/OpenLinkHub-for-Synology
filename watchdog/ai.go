package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Analyse par l'IA (Claude) des événements du NAS: texte clair en français et tri.
//
// Règles de sécurité, voulues par l'utilisateur:
//   - l'IA n'a aucun pouvoir de décision sur l'extinction du NAS (ce code ne lui en donne aucun);
//   - elle ne peut jamais rétrograder ni retenir une alerte critique ou haute (applyAI);
//   - si elle est indisponible, les alertes partent avec le texte standard, « non analysé ».
//
// Les faits (mesures, compteurs, états) viennent des capteurs et sont fournis par ce programme: l'IA
// rédige, elle ne mesure pas. Elle ne reçoit ni adresse IP, ni nom d'hôte, ni numéro de série.

type aiOut struct {
	Key      string `json:"cle"`
	Title    string `json:"titre"`
	Summary  string `json:"resume"`
	Action   string `json:"action"`
	Why      string `json:"pourquoi"`
	Gravity  string `json:"gravite"`
	Relevant bool   `json:"pertinent"`
}

type aiResult struct {
	Title string
	Intro string
	Cards map[string]aiOut
}

type aiStats struct {
	Enabled     bool
	Model       string
	CallsToday  int
	MaxPerDay   int
	Failures    int
	TokensIn    int
	TokensOut   int
	LastError   string
	LastSuccess time.Time
}

// aiAnalyzer est ce dont le répartiteur a besoin (remplaçable dans les tests).
type aiAnalyzer interface {
	analyze(ctx context.Context, kind string, cards []card, state, known, recent []string) (*aiResult, error)
	stats() aiStats
}

const aiSystem = `Tu es l'assistant de surveillance d'un NAS Synology DS1825+ installé dans un meuble refroidi, chez un particulier averti mais non spécialiste. Tu rédiges en français clair les alertes que le programme de surveillance a déjà détectées avec des seuils fixes.

Installation: 8 disques Seagate de 12 To en RAID 6 (le volume supporte la perte de deux disques), baies toutes occupées; une réplication vers un second NAS (DS1522+) sert de sauvegarde; un onduleur Eaton 5P protège le NAS; le meuble est ventilé par 4 ventilateurs Corsair iCUE LINK avec deux sondes de température (une dans le NAS, une dans la baie d'un boîtier QNAP). Quand le NAS s'éteint, tout le meuble est protégé. La surveillance peut être en « mode à blanc »: elle prévient sans éteindre le NAS. Une imprimante 3D (Snapmaker) se trouve dans le même meuble, au-dessus du NAS, mais elle est éteinte et inutilisée depuis plus d'un mois: ne la cite pas comme cause d'un événement récent.

Tu reçois un bloc JSON: « cartes » (les alertes, avec un constat, des mesures et une gravité déterministe), « etat_du_nas » (l'état général, pour rapprocher les signaux), « situations_connues » (ce que l'utilisateur sait déjà) et « evenements_recents » (les événements des dernières 24 h, du plus ancien au plus récent).

Rapprochement des événements: plusieurs événements isolés rapprochés dans le temps, sur des disques différents et sans erreur de lecture, suggèrent une cause commune plutôt que des pannes séparées (vibration, alimentation). Dis-le avec prudence (« cause commune possible ») et propose de vérifier ce qui fonctionnait à ces heures près du meuble (lave-linge, sèche-linge, passage, portes) et les tâches planifiées de DSM (sauvegarde, réplication, indexation), sans jamais l'affirmer. Un événement unique ne justifie pas ce rapprochement.

Règles impératives:
- N'écris QUE des chiffres, noms et faits présents dans le bloc reçu. N'invente aucune cause, aucune version, aucun numéro. Si la cause n'est pas établie par les données, dis-le (« cause non établie »).
- Ne recommande jamais d'action risquée pour les données: jamais de retirer un disque avant l'arrivée du remplaçant, jamais de lancer plusieurs opérations lourdes à la fois, jamais d'éteindre brutalement le NAS. Un disque se remplace un seul à la fois.
- Rapproche les signaux: un événement isolé sur un seul disque sans erreur n'est pas une coupure de courant; un seul ventilateur arrêté sur quatre ne met pas le NAS en danger immédiat, etc.
- gravite: critique, haute, moyenne ou basse. Tu peux relever la gravité déterministe, jamais l'abaisser pour une carte critique ou haute. Pour une carte moyenne ou basse, tu peux la monter ou la baisser.
- pertinent: faux seulement pour une information sans intérêt pour l'utilisateur (déjà connue, sans conséquence, redondante). Une carte critique ou haute est toujours pertinente.
- titre: 10 mots au plus, dit quel équipement et ce qui se passe. resume: 2 à 3 phrases simples. action: une action concrète (ou « Rien de particulier à faire. »). pourquoi: une phrase.
- Champs globaux: titre (10 mots au plus) et intro (une phrase, ou une chaîne vide). Pour une alerte, l'intro est vide.

Réponds uniquement avec le JSON demandé, une entrée par carte reçue, avec la même clé « cle ».`

var aiSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"titre": map[string]any{"type": "string"},
		"intro": map[string]any{"type": "string"},
		"cartes": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"cle":       map[string]any{"type": "string"},
					"titre":     map[string]any{"type": "string"},
					"resume":    map[string]any{"type": "string"},
					"action":    map[string]any{"type": "string"},
					"pourquoi":  map[string]any{"type": "string"},
					"gravite":   map[string]any{"type": "string", "enum": []string{"critique", "haute", "moyenne", "basse"}},
					"pertinent": map[string]any{"type": "boolean"},
				},
				"required":             []string{"cle", "titre", "resume", "action", "pourquoi", "gravite", "pertinent"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"titre", "intro", "cartes"},
	"additionalProperties": false,
}

// anthropicAI appelle l'API Messages d'Anthropic en HTTPS (sans SDK).
type anthropicAI struct {
	key       string
	model     string
	base      string
	hc        *http.Client
	maxPerDay int

	mu       sync.Mutex
	day      string
	calls    int
	failures int
	inTok    int
	outTok   int
	lastErr  string
	lastOK   time.Time
	noSchema bool // l'API a refusé le format imposé: on s'en passe et on valide nous-mêmes
}

// newAnthropic renvoie nil sans clé: la surveillance fonctionne alors sans IA.
func newAnthropic(env map[string]string, maxPerDay int) *anthropicAI {
	key := env["ANTHROPIC_API_KEY"]
	if key == "" {
		return nil
	}
	model := env["AI_MODEL"]
	if model == "" {
		model = "claude-haiku-5-5"
	}
	return &anthropicAI{key: key, model: model, base: "https://api.anthropic.com", maxPerDay: maxPerDay,
		hc: &http.Client{Timeout: 90 * time.Second}}
}

func (a *anthropicAI) stats() aiStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return aiStats{Enabled: true, Model: a.model, CallsToday: a.calls, MaxPerDay: a.maxPerDay, Failures: a.failures,
		TokensIn: a.inTok, TokensOut: a.outTok, LastError: a.lastErr, LastSuccess: a.lastOK}
}

func (a *anthropicAI) reserve() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	today := time.Now().Format("2006-01-02")
	if a.day != today {
		a.day, a.calls = today, 0
	}
	if a.maxPerDay > 0 && a.calls >= a.maxPerDay {
		return fmt.Errorf("plafond de %d appels par jour atteint", a.maxPerDay)
	}
	a.calls++
	return nil
}

func (a *anthropicAI) fail(err error) error {
	a.mu.Lock()
	a.failures++
	a.lastErr = scrub(err.Error(), a.key)
	a.mu.Unlock()
	return err
}

type aiCardIn struct {
	Key           string      `json:"cle"`
	StandardGrave string      `json:"gravite_deterministe"`
	Finding       string      `json:"constat"`
	StandardTitle string      `json:"titre_standard"`
	Measures      []aiMeasure `json:"mesures,omitempty"`
	Equip         string      `json:"equipement,omitempty"`
}

type aiMeasure struct {
	Label string `json:"etiquette"`
	Value string `json:"valeur"`
}

func aiRequestBody(kind string, cards []card, state, known, recent []string) string {
	var in []aiCardIn
	for _, c := range cards {
		x := aiCardIn{Key: c.Key, StandardGrave: c.Gravity, Finding: c.Summary, StandardTitle: c.Title, Equip: c.Equip}
		for _, m := range c.Measures {
			x.Measures = append(x.Measures, aiMeasure{m.Label, m.Value})
		}
		in = append(in, x)
	}
	b, _ := json.MarshalIndent(map[string]any{
		"type": kind, "cartes": in, "etat_du_nas": state, "situations_connues": known, "evenements_recents": recent,
	}, "", " ")
	return "<donnees>\n" + string(b) + "\n</donnees>\n\nRédige les alertes de ces cartes."
}

func (a *anthropicAI) analyze(ctx context.Context, kind string, cards []card, state, known, recent []string) (*aiResult, error) {
	if len(cards) == 0 {
		return &aiResult{Cards: map[string]aiOut{}}, nil
	}
	if err := a.reserve(); err != nil {
		return nil, err
	}
	user := aiRequestBody(kind, cards, state, known, recent)
	text, err := a.call(ctx, user, !a.schemaOff())
	if err != nil && a.schemaRejected(err) {
		text, err = a.call(ctx, user, false)
	}
	if err != nil {
		return nil, a.fail(err)
	}
	res, err := parseAIResult(text, cards)
	if err != nil {
		return nil, a.fail(err)
	}
	a.mu.Lock()
	a.lastOK, a.lastErr = time.Now(), ""
	a.mu.Unlock()
	return res, nil
}

func (a *anthropicAI) schemaOff() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.noSchema
}

type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string { return fmt.Sprintf("API Anthropic: HTTP %d: %s", e.Status, e.Msg) }

func (a *anthropicAI) schemaRejected(err error) bool {
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == http.StatusBadRequest && strings.Contains(ae.Msg, "output_config") {
		a.mu.Lock()
		a.noSchema = true
		a.mu.Unlock()
		return true
	}
	return false
}

func (a *anthropicAI) call(ctx context.Context, user string, withSchema bool) (string, error) {
	body := map[string]any{
		"model": a.model, "max_tokens": 3000, "system": aiSystem,
		"messages": []map[string]any{{"role": "user", "content": user}},
	}
	if withSchema {
		body["output_config"] = map[string]any{"effort": "low", "format": map[string]any{"type": "json_schema", "schema": aiSchema}}
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.base, "/")+"/v1/messages", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", a.key)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := a.hc.Do(req)
	if err != nil {
		return "", errors.New(scrub(err.Error(), a.key))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			In  int `json:"input_tokens"`
			Out int `json:"output_tokens"`
		} `json:"usage"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &r)
	if resp.StatusCode != http.StatusOK {
		msg := r.Error.Type
		if r.Error.Message != "" {
			msg += " " + short(r.Error.Message, 200)
		}
		return "", &apiError{Status: resp.StatusCode, Msg: scrub(msg, a.key)}
	}
	a.mu.Lock()
	a.inTok += r.Usage.In
	a.outTok += r.Usage.Out
	a.mu.Unlock()
	switch r.StopReason {
	case "refusal":
		return "", errors.New("le modèle a refusé de répondre")
	case "max_tokens":
		return "", errors.New("réponse tronquée (max_tokens atteint)")
	}
	for _, c := range r.Content {
		if c.Type == "text" {
			return c.Text, nil
		}
	}
	return "", errors.New("réponse sans texte")
}

// short réduit les espaces et limite la longueur (en caractères, pas en octets).
func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// parseAIResult valide la réponse: JSON bien formé, clés connues, gravités valides, textes bornés.
func parseAIResult(text string, cards []card) (*aiResult, error) {
	text = strings.TrimSpace(text)
	// tolère une clôture ```json … ``` si le format imposé n'a pas pu être utilisé
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	var raw struct {
		Title string  `json:"titre"`
		Intro string  `json:"intro"`
		Cards []aiOut `json:"cartes"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &raw); err != nil {
		return nil, fmt.Errorf("JSON invalide (%v)", err)
	}
	known := map[string]bool{}
	for _, c := range cards {
		known[c.Key] = true
	}
	res := &aiResult{Title: short(raw.Title, 120), Intro: short(raw.Intro, 240), Cards: map[string]aiOut{}}
	for _, o := range raw.Cards {
		if !known[o.Key] {
			continue
		}
		if _, dup := res.Cards[o.Key]; dup {
			continue
		}
		if gravRank(o.Gravity) >= len(gravOrder) {
			o.Gravity = ""
		}
		o.Title, o.Summary = short(o.Title, 120), short(o.Summary, 700)
		o.Action, o.Why = short(o.Action, 400), short(o.Why, 300)
		res.Cards[o.Key] = o
	}
	return res, nil
}

// applyAI fusionne l'analyse avec les cartes. Retourne les cartes gardées et celles écartées.
// Une carte critique ou haute ne peut ni descendre en gravité ni être écartée.
func applyAI(cards []card, res *aiResult) (kept, discarded []card) {
	for _, c := range cards {
		o, ok := res.Cards[c.Key]
		if !ok {
			kept = append(kept, c)
			continue
		}
		base := c.Gravity
		g := o.Gravity
		if g == "" {
			g = base
		}
		if (base == "critique" || base == "haute") && gravRank(g) > gravRank(base) {
			g = base
		}
		if o.Title != "" {
			c.Title = o.Title
		}
		if o.Summary != "" {
			c.Summary = o.Summary
		}
		if o.Action != "" {
			c.Action = o.Action
		}
		if o.Why != "" {
			c.Why = o.Why
		}
		c.Gravity, c.Analyzed = g, true
		if !o.Relevant && (g == "moyenne" || g == "basse") && (base == "moyenne" || base == "basse") {
			discarded = append(discarded, c)
			continue
		}
		kept = append(kept, c)
	}
	return kept, discarded
}

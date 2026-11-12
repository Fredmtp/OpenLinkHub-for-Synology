package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- rendu HTML ----

func sampleCard(g string) card {
	return card{Key: "disk-bad-sata2", Gravity: g, Where: "Matériel : disque 2", Title: "Disque 2 : secteurs illisibles",
		Summary: "Résumé.", Action: "Remplacer.", Measures: []measure{{"Secteurs en attente", "40", "crit"}}}
}

func TestRenderAlertMailStructure(t *testing.T) {
	subject, html := renderAlertMail(time.Date(2026, 10, 26, 7, 52, 0, 0, time.Local), []card{sampleCard("critique")},
		mailOpts{Dry: true, AI: true, PageURL: "http://192.168.1.10:27004/"})
	for _, want := range []string{"NAS meuble", "ALERTE IMMÉDIATE", "CRITIQUE", "À FAIRE", "#8E1B2E", "MODE À BLANC",
		"lundi 26/10/2026 · 07:52", "Page d'état", "analyse par l&#39;IA", "Secteurs en attente"} {
		if !strings.Contains(html, want) {
			t.Errorf("le mail ne contient pas %q", want)
		}
	}
	if subject != "[NAS meuble] Disque 2 : secteurs illisibles" {
		t.Fatalf("sujet: %s", subject)
	}
}

func TestRenderEscapesEverything(t *testing.T) {
	c := sampleCard("haute")
	c.Title = "<script>alert(1)</script>"
	c.Summary = `<img src=x onerror="alert(2)">`
	c.Why = "</td></tr></table><a href='https://piege.example'>clic</a>"
	c.Measures = []measure{{"<b>x</b>", "<i>y</i>", ""}}
	_, html := renderAlertMail(time.Now(), []card{c}, mailOpts{})
	for _, bad := range []string{"<script>", "<img", "<b>x", "<i>y", "href='https://piege"} {
		if strings.Contains(html, bad) {
			t.Fatalf("HTML non échappé: %s", bad)
		}
	}
	if !strings.Contains(html, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatal("le titre doit apparaître échappé")
	}
}

func TestRenderCardsSortedBySeverity(t *testing.T) {
	a, b := sampleCard("basse"), sampleCard("critique")
	a.Key, a.Title = "a", "TITRE-BASSE"
	b.Key, b.Title = "b", "TITRE-CRITIQUE"
	_, html := renderAlertMail(time.Now(), []card{a, b}, mailOpts{})
	if strings.Index(html, "TITRE-CRITIQUE") > strings.Index(html, "TITRE-BASSE") {
		t.Fatal("la carte critique doit venir avant la carte d'information")
	}
	if !strings.Contains(html, "2 points demandent votre attention") {
		t.Fatal("titre global attendu pour plusieurs cartes")
	}
}

func TestRenderDigestKnownSectionOnlyWhenShown(t *testing.T) {
	d := digestData{Known: []knownItem{{"Disque 2 : défauts", "remplacement prévu"}, {"Disque 4 : défauts", "test à faire"}}}
	_, h1 := renderDigestMail(time.Now(), d, mailOpts{Hour: "13 H"})
	if strings.Contains(h1, "Déjà connus") || !strings.Contains(h1, "Pris en compte · 2") {
		t.Fatal("hors rappel hebdomadaire: la pastille seule, pas la section")
	}
	d.KnownShown = true
	_, h2 := renderDigestMail(time.Now(), d, mailOpts{Hour: "13 H"})
	if !strings.Contains(h2, "Déjà connus") || !strings.Contains(h2, "Disque 4 : défauts") {
		t.Fatal("la section doit apparaître au rappel hebdomadaire")
	}
}

func TestFrenchNumberFormats(t *testing.T) {
	if got := frInt(69592); got != "69 592" {
		t.Fatal(got)
	}
	if got := frInt(5); got != "5" {
		t.Fatal(got)
	}
	if got := fr1(28.84); got != "28,8" {
		t.Fatal(got)
	}
}

// ---- textes standard ----

func TestDescribeDiskCardHasFactsAndAdvice(t *testing.T) {
	in := input()
	in.NAS.Disks[1].Pending, in.NAS.Disks[1].Reallocated, in.NAS.Disks[1].Reported = 40, 69592, 1087
	in.NAS.Disks[1].Model, in.NAS.Disks[1].PowerOn = "ST12000VN0008-2JH101", 5207
	c := describeFinding(finding{Key: "disk-bad-sata2", Level: levelCrit, Text: "disque sata2: 40 secteur(s) en attente"}, in)
	if c.Gravity != "critique" || c.Where != "Matériel : disque 2" || !strings.Contains(c.Meta, "5 207 h") {
		t.Fatalf("%+v", c)
	}
	if !strings.Contains(c.Action, "un seul disque à la fois") {
		t.Fatalf("la consigne de remplacement un par un est obligatoire: %s", c.Action)
	}
	found := false
	for _, m := range c.Measures {
		if m.Label == "Secteurs retirés" && m.Value == "69 592" {
			found = true
		}
	}
	if !found {
		t.Fatalf("mesures: %+v", c.Measures)
	}
}

func TestBaseGravity(t *testing.T) {
	cases := []struct {
		key, level, want string
	}{
		{"disk-bad-sata2", levelCrit, "critique"},
		{"raid-md2-severe", levelCrit, "critique"},
		{"disk-temp-sata1", levelWarn, "haute"},
		{"corsair-fan-2", levelWarn, "haute"},
		{"ups", levelWarn, "haute"},
		{"disk-errors-sata4", levelWarn, "moyenne"},
		{"disk-realloc-sata4", levelWarn, "moyenne"},
		{"volume", levelWarn, "moyenne"},
		{"probe-NAS", levelWarn, "moyenne"},
		{"raid-md2", levelWarn, "moyenne"},
		{"power-isolated", levelWarn, "basse"},
		{"power-general", levelWarn, "haute"},
	}
	for _, c := range cases {
		if got := baseGravity(finding{Key: c.key, Level: c.level}); got != c.want {
			t.Errorf("%s/%s: obtenu %s, attendu %s", c.key, c.level, got, c.want)
		}
	}
}

func TestEveryFindingKeyHasStandardText(t *testing.T) {
	keys := []string{"disk-temp-sata1", "disk-health-sata1", "disk-bad-sata1", "disk-growth-sata1", "disk-errors-sata1",
		"disk-realloc-sata1", "cpu-temp", "board-temp", "nasfan-all", "nasfan-1", "corsair-all", "corsair-fan-3",
		"corsair-missing", "probe-NAS", "probe-QNAP-missing", "ups", "raid-md2", "raid-md2-severe", "volume", "api",
		"nas-stale", "power-isolated", "power-general", "cle-inconnue"}
	for _, k := range keys {
		c := describeFinding(finding{Key: k, Level: levelWarn, Text: "texte du constat"}, input())
		if c.Title == "" || c.Action == "" || c.Where == "" || c.Gravity == "" {
			t.Errorf("%s: carte incomplète %+v", k, c)
		}
	}
}

// ---- IA: client et validation ----

func aiServer(t *testing.T, handler http.HandlerFunc) (*anthropicAI, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &anthropicAI{key: "sk-ant-SECRET", model: "claude-haiku-5-5", base: srv.URL, hc: srv.Client(), maxPerDay: 10}, srv
}

func apiOK(text string) string {
	b, _ := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}}, "stop_reason": "end_turn",
		"usage": map[string]any{"input_tokens": 1200, "output_tokens": 300}})
	return string(b)
}

const goodAnswer = `{"titre":"Disque 2 : surface abîmée","intro":"","cartes":[{"cle":"disk-bad-sata2","titre":"Disque 2 : secteurs illisibles",` +
	`"resume":"Le disque 2 a 40 secteurs illisibles.","action":"Remplacer le disque.","pourquoi":"Défaut confirmé.","gravite":"critique","pertinent":true}]}`

func TestAnthropicCallAndHeaders(t *testing.T) {
	var gotKey, gotVersion string
	var body map[string]any
	a, _ := aiServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotVersion = r.Header.Get("x-api-key"), r.Header.Get("anthropic-version")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		io.WriteString(w, apiOK(goodAnswer))
	})
	res, err := a.analyze(context.Background(), "alerte", []card{sampleCard("critique")}, []string{"RAID sain"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "sk-ant-SECRET" || gotVersion == "" || body["model"] != "claude-haiku-5-5" || body["output_config"] == nil {
		t.Fatalf("requête incorrecte: key=%q version=%q body=%v", gotKey, gotVersion, body)
	}
	if res.Cards["disk-bad-sata2"].Action != "Remplacer le disque." {
		t.Fatalf("%+v", res)
	}
	user := body["messages"].([]any)[0].(map[string]any)["content"].(string)
	for _, forbidden := range []string{"192.168", "ZLW2", "B855FDBE"} {
		if strings.Contains(user, forbidden) {
			t.Fatalf("la requête ne doit contenir ni adresse ni numéro de série: %s", forbidden)
		}
	}
	if s := a.stats(); s.TokensIn != 1200 || s.CallsToday != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestAnthropicErrorNeverLeaksKey(t *testing.T) {
	a, _ := aiServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"type":"authentication_error","message":"invalid x-api-key sk-ant-SECRET"}}`)
	})
	_, err := a.analyze(context.Background(), "alerte", []card{sampleCard("critique")}, nil, nil, nil)
	if err == nil || strings.Contains(err.Error(), "sk-ant-SECRET") || strings.Contains(a.stats().LastError, "sk-ant-SECRET") {
		t.Fatalf("la clé ne doit jamais apparaître: %v / %q", err, a.stats().LastError)
	}
}

func TestAnthropicSchemaRejectedFallsBack(t *testing.T) {
	calls, withSchema := 0, 0
	a, _ := aiServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		if body["output_config"] != nil {
			withSchema++
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"output_config: not supported"}}`)
			return
		}
		io.WriteString(w, apiOK("```json\n"+goodAnswer+"\n```"))
	})
	if _, err := a.analyze(context.Background(), "alerte", []card{sampleCard("critique")}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || withSchema != 1 || !a.schemaOff() {
		t.Fatalf("un seul essai avec format, puis sans: calls=%d avec=%d", calls, withSchema)
	}
	if _, err := a.analyze(context.Background(), "alerte", []card{sampleCard("critique")}, nil, nil, nil); err != nil || calls != 3 {
		t.Fatalf("le format refusé ne doit plus être redemandé: calls=%d err=%v", calls, err)
	}
}

func TestAnthropicBudget(t *testing.T) {
	a, _ := aiServer(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, apiOK(goodAnswer)) })
	a.maxPerDay = 1
	if _, err := a.analyze(context.Background(), "alerte", []card{sampleCard("critique")}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.analyze(context.Background(), "alerte", []card{sampleCard("critique")}, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "plafond") {
		t.Fatalf("le plafond quotidien doit s'appliquer: %v", err)
	}
}

func TestAnthropicRefusalAndTruncation(t *testing.T) {
	for _, stop := range []string{"refusal", "max_tokens"} {
		a, _ := aiServer(t, func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"content":[{"type":"text","text":"{}"}],"stop_reason":"`+stop+`","usage":{}}`)
		})
		if _, err := a.analyze(context.Background(), "alerte", []card{sampleCard("critique")}, nil, nil, nil); err == nil {
			t.Errorf("stop_reason %s doit être une erreur", stop)
		}
	}
}

func TestParseAIResultValidation(t *testing.T) {
	cards := []card{sampleCard("critique")}
	raw := `{"titre":"T","intro":"I","cartes":[
	  {"cle":"inconnue","titre":"x","resume":"x","action":"x","pourquoi":"x","gravite":"critique","pertinent":true},
	  {"cle":"disk-bad-sata2","titre":"` + strings.Repeat("a", 500) + `","resume":"r","action":"a","pourquoi":"p","gravite":"extreme","pertinent":true},
	  {"cle":"disk-bad-sata2","titre":"doublon","resume":"r","action":"a","pourquoi":"p","gravite":"basse","pertinent":false}]}`
	res, err := parseAIResult(raw, cards)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Cards) != 1 {
		t.Fatalf("clé inconnue et doublon ignorés: %+v", res.Cards)
	}
	o := res.Cards["disk-bad-sata2"]
	if len([]rune(o.Title)) > 120 || o.Gravity != "" || o.Title == "doublon" {
		t.Fatalf("titre borné, gravité invalide vidée, premier gagne: %+v", o)
	}
	if _, err := parseAIResult("pas du json", cards); err == nil {
		t.Fatal("JSON invalide: erreur attendue")
	}
}

func TestApplyAINeverDowngradesCriticalOrHigh(t *testing.T) {
	mk := func(key, g string) card { c := sampleCard(g); c.Key = key; return c }
	cards := []card{mk("c", "critique"), mk("h", "haute"), mk("m", "moyenne"), mk("b", "basse"), mk("m2", "moyenne")}
	res := &aiResult{Cards: map[string]aiOut{
		"c":  {Key: "c", Title: "t", Gravity: "basse", Relevant: false},    // tente de rétrograder et d'écarter
		"h":  {Key: "h", Title: "t", Gravity: "moyenne", Relevant: false},  // idem
		"m":  {Key: "m", Title: "t", Gravity: "basse", Relevant: false},    // peut être écartée
		"b":  {Key: "b", Title: "t", Gravity: "haute", Relevant: true},     // peut être relevée
		"m2": {Key: "m2", Title: "t", Gravity: "critique", Relevant: true}, // peut être relevée
	}}
	kept, disc := applyAI(cards, res)
	got := map[string]string{}
	for _, c := range kept {
		got[c.Key] = c.Gravity
	}
	if got["c"] != "critique" || got["h"] != "haute" {
		t.Fatalf("une alerte critique ou haute ne doit ni baisser ni être retenue: %v", got)
	}
	if got["b"] != "haute" || got["m2"] != "critique" {
		t.Fatalf("l'IA peut relever une alerte moyenne ou basse: %v", got)
	}
	if len(disc) != 1 || disc[0].Key != "m" {
		t.Fatalf("seule la carte moyenne jugée sans intérêt est écartée: %+v", disc)
	}
}

// ---- répartiteur ----

type fakeAI struct {
	mu         sync.Mutex
	res        *aiResult
	err        error
	calls      int
	lastRecent []string
}

func (f *fakeAI) analyze(ctx context.Context, kind string, cards []card, state, known, recent []string) (*aiResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastRecent = recent
	if f.err != nil {
		return nil, f.err
	}
	return f.res, nil
}
func (f *fakeAI) stats() aiStats { return aiStats{Enabled: true} }

type sink struct {
	mu   sync.Mutex
	msgs []message
}

func (s *sink) send(m message) { s.mu.Lock(); s.msgs = append(s.msgs, m); s.mu.Unlock() }

func newTestDispatcher(ai aiAnalyzer, known map[string]string) (*dispatcher, *sink) {
	s := &sink{}
	in := input()
	h := newHistory()
	h.add(at(0), metrics(in, 13, 14))
	d := newDispatcher(s.send, ai, true, "http://192.168.1.10:27004/", known,
		func() (assessInput, []finding) { return in, nil }, h, 8)
	return d, s
}

func item(kind, key, level, text string) alertItem {
	return alertItem{Kind: kind, F: finding{Key: key, Level: level, Text: text}}
}

func TestDispatchCriticalIsImmediateEvenWhenAIFails(t *testing.T) {
	d, s := newTestDispatcher(&fakeAI{err: errors.New("API indisponible")}, nil)
	d.process([]alertItem{item("fresh", "disk-bad-sata2", levelCrit, "disque sata2: 40 secteur(s) en attente")})
	if len(s.msgs) != 1 {
		t.Fatalf("une alerte critique doit partir même sans IA: %d message(s)", len(s.msgs))
	}
	m := s.msgs[0]
	if m.Priority != 5 || m.HTML == "" || !strings.Contains(m.HTML, "Non analysé par l&#39;IA") || !strings.Contains(m.Short, "CRITIQUE") {
		t.Fatalf("%+v", m)
	}
}

func TestDispatchWithoutAIStillSends(t *testing.T) {
	d, s := newTestDispatcher(nil, nil)
	d.process([]alertItem{item("fresh", "corsair-fan-2", levelWarn, "ventilateur du meuble canal 2 arrêté")})
	if len(s.msgs) != 1 || s.msgs[0].Priority != 4 || strings.Contains(s.msgs[0].HTML, "Non analysé") {
		t.Fatalf("haute = immédiat, et sans IA aucune mention « non analysé »: %+v", s.msgs)
	}
}

func TestDispatchMediumWaitsForDigest(t *testing.T) {
	d, s := newTestDispatcher(nil, nil)
	d.process([]alertItem{item("fresh", "volume", levelWarn, "volume 1 rempli à 91 %")})
	if len(s.msgs) != 0 || len(d.pending) != 1 {
		t.Fatalf("une alerte moyenne attend le résumé: msgs=%d pending=%d", len(s.msgs), len(d.pending))
	}
	d.digest(time.Date(2026, 10, 27, 13, 0, 0, 0, time.Local))
	if len(s.msgs) != 1 || !strings.Contains(s.msgs[0].HTML, "À savoir") || !strings.Contains(s.msgs[0].HTML, "Le volume 1 se remplit") {
		t.Fatalf("le résumé doit contenir la carte en attente: %+v", s.msgs)
	}
	if len(d.pending) != 0 {
		t.Fatal("les cartes envoyées dans un résumé ne doivent pas être renvoyées")
	}
}

func TestDispatchKnownNeverPushedButSummarisedWeekly(t *testing.T) {
	known := map[string]string{"disk-bad-sata2": "remplacement prévu"}
	d, s := newTestDispatcher(nil, known)
	d.process([]alertItem{item("fresh", "disk-bad-sata2", levelCrit, "disque sata2: 40 secteur(s) en attente"),
		item("remind", "disk-bad-sata2", levelCrit, "disque sata2: 40 secteur(s) en attente")})
	if len(s.msgs) != 0 {
		t.Fatalf("une situation connue n'est jamais poussée: %+v", s.msgs)
	}
	// l'anomalie est toujours active: le résumé du lundi 8 h la rappelle
	in := input()
	in.NAS.Disks[1].Pending = 40
	d.state = func() (assessInput, []finding) {
		return in, []finding{{Key: "disk-bad-sata2", Level: levelCrit, Text: "disque sata2: 40 secteur(s) en attente"}}
	}
	d.digest(time.Date(2026, 10, 26, 8, 0, 0, 0, time.Local)) // lundi, 1er résumé
	if !strings.Contains(s.msgs[0].HTML, "Déjà connus") || !strings.Contains(s.msgs[0].HTML, "Tout fonctionne, 1 point(s) restent sous surveillance") {
		t.Fatalf("rappel hebdomadaire attendu le lundi à 8 h: %s", s.msgs[0].Subject)
	}
	d.digest(time.Date(2026, 10, 27, 13, 0, 0, 0, time.Local)) // mardi
	if strings.Contains(s.msgs[1].HTML, "Déjà connus") {
		t.Fatal("pas de rappel détaillé les autres jours")
	}
}

func TestDispatchAICanPromoteALowAlert(t *testing.T) {
	ai := &fakeAI{res: &aiResult{Title: "t", Cards: map[string]aiOut{
		"power-isolated": {Key: "power-isolated", Title: "Événement à surveiller", Summary: "Deuxième parcage en une heure.",
			Action: "Vérifier la baie.", Gravity: "haute", Relevant: true}}}}
	d, s := newTestDispatcher(ai, nil)
	d.process([]alertItem{item("event", "power-isolated", levelWarn, "retraits d'urgence en hausse sur 1 disque sur 8")})
	if len(s.msgs) != 1 || s.msgs[0].Priority != 4 || !strings.Contains(s.msgs[0].HTML, "Événement à surveiller") {
		t.Fatalf("l'IA peut relever une information en alerte immédiate: %+v", s.msgs)
	}
}

func TestDispatchAIDiscardsHarmlessInfo(t *testing.T) {
	ai := &fakeAI{res: &aiResult{Cards: map[string]aiOut{
		"power-isolated": {Key: "power-isolated", Title: "Parcage isolé", Gravity: "basse", Relevant: false}}}}
	d, s := newTestDispatcher(ai, nil)
	d.process([]alertItem{item("event", "power-isolated", levelWarn, "retraits d'urgence en hausse sur 1 disque sur 8")})
	if len(s.msgs) != 0 || len(d.pending) != 0 || len(d.pendingDisc) != 1 {
		t.Fatalf("écartée: ni push ni carte, mais listée pour contrôle: msgs=%d pending=%d disc=%d", len(s.msgs), len(d.pending), len(d.pendingDisc))
	}
	d.digest(time.Date(2026, 10, 27, 19, 0, 0, 0, time.Local))
	if !strings.Contains(s.msgs[0].HTML, "Écartés par l&#39;IA, pour contrôle") {
		t.Fatal("les cartes écartées doivent figurer dans le résumé pour contrôle")
	}
}

func TestDispatchRecoveryOfCriticalIsPushed(t *testing.T) {
	d, s := newTestDispatcher(nil, nil)
	d.process([]alertItem{item("back", "raid-md2-severe", levelCrit, "RAID md2 SANS REDONDANCE")})
	if len(s.msgs) != 1 || !strings.Contains(s.msgs[0].HTML, "Rétabli") {
		t.Fatalf("le retour à la normale d'une alerte critique part tout de suite: %+v", s.msgs)
	}
	d2, s2 := newTestDispatcher(nil, nil)
	d2.process([]alertItem{item("back", "volume", levelWarn, "volume 1 rempli à 91 %")})
	if len(s2.msgs) != 0 {
		t.Fatal("le retour à la normale d'une alerte moyenne attend le résumé")
	}
}

func TestDigestOfAQuietDayDoesNotCallAI(t *testing.T) {
	ai := &fakeAI{res: &aiResult{}}
	d, s := newTestDispatcher(ai, nil)
	d.digest(time.Date(2026, 10, 27, 13, 0, 0, 0, time.Local))
	if ai.calls != 0 {
		t.Fatal("un résumé sans événement ne coûte aucun appel à l'IA")
	}
	m := s.msgs[0]
	if !strings.Contains(m.Subject, "Tout fonctionne") || !strings.Contains(m.HTML, "Disques") || !strings.Contains(m.HTML, "Mesures des dernières 24 h") {
		t.Fatalf("%s", m.Subject)
	}
}

func TestDispatchPlainMessagesPassThrough(t *testing.T) {
	d, s := newTestDispatcher(nil, nil)
	d.post(message{Subject: "simple", Body: "x", Short: "x"})
	if len(s.msgs) != 1 || s.msgs[0].Subject != "simple" {
		t.Fatal("un message sans événement structuré passe tel quel")
	}
}

func TestFixedMessages(t *testing.T) {
	m := shutdownMessage("aucun ventilateur en rotation depuis 1m0s", "http://x/")
	if m.Priority != 5 || !strings.Contains(m.HTML, "Arrêt automatique du NAS") || !strings.Contains(m.HTML, "ARRÊT AUTOMATIQUE") || !strings.Contains(m.Short, "aucun ventilateur") {
		t.Fatalf("%+v", m)
	}
	u := startupMessage(3*time.Hour, true, "http://x/")
	if u.Priority != 2 || !strings.Contains(u.HTML, "MODE À BLANC") {
		t.Fatalf("%+v", u)
	}
}

// ---- journal des événements ----

func TestEventLogPersistsAndPrunes(t *testing.T) {
	path := t.TempDir() + "/evenements.jsonl"
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local)
	l := newEventLog(path, now)
	l.add(eventRec{At: now.Add(-30 * time.Minute), Kind: "event", Key: "power-isolated", Title: "t1", Text: "sata1 +1", Gravity: "basse"})
	l.add(eventRec{At: now.Add(-10 * 24 * time.Hour), Kind: "event", Key: "vieux", Title: "vieux", Text: "x", Gravity: "basse"})
	// Rechargement: l'événement récent survit, celui de plus de 7 jours disparaît (et du fichier aussi).
	l2 := newEventLog(path, now)
	got := l2.since(now.Add(-24 * time.Hour))
	if len(got) != 1 || got[0].Title != "t1" {
		t.Fatalf("%+v", got)
	}
	l3 := newEventLog(path, now)
	if len(l3.since(now.Add(-30*24*time.Hour))) != 1 {
		t.Fatal("l'événement ancien aurait dû être retiré du fichier")
	}
	var nilLog *eventLog
	nilLog.add(eventRec{}) // sans journal configuré: aucune panique
	if len(nilLog.since(now)) != 0 {
		t.Fatal("un journal nil est vide")
	}
}

func TestEventLogIgnoresGarbageLines(t *testing.T) {
	path := t.TempDir() + "/e.jsonl"
	now := time.Now()
	good := `{"at":"` + now.Add(-time.Hour).Format(time.RFC3339) + `","kind":"event","key":"k","titre":"ok","texte":"x","gravite":"basse"}`
	if err := os.WriteFile(path, []byte("pas du json\n"+good+"\n{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := newEventLog(path, now)
	if got := l.since(now.Add(-24 * time.Hour)); len(got) != 1 || got[0].Title != "ok" {
		t.Fatalf("seules les lignes valides sont gardées: %+v", got)
	}
}

func TestAIReceivesRecentEventsAndCorrelatesMorningEvents(t *testing.T) {
	// Cas du 26/10/2026: sata1 à 08:08 puis sata7 à 09:23, disques différents.
	now := time.Date(2026, 10, 26, 9, 24, 0, 0, time.Local)
	ai := &fakeAI{res: &aiResult{Cards: map[string]aiOut{}}}
	d, s := newTestDispatcher(ai, nil)
	d.now = func() time.Time { return now }
	d.events = newEventLog("", now)
	d.events.add(eventRec{At: time.Date(2026, 10, 26, 8, 8, 0, 0, time.Local), Kind: "event", Key: "power-isolated",
		Title: "Un disque a parqué ses têtes de lui-même", Text: "retraits d'urgence en hausse sur 1 disque(s) sur 8 (sata1 +1)", Gravity: "basse"})
	d.process([]alertItem{item("event", "power-isolated", levelWarn, "retraits d'urgence en hausse sur 1 disque(s) sur 8 (sata7 +1)")})
	if len(ai.lastRecent) != 2 || !strings.Contains(ai.lastRecent[0], "sata1 +1") || !strings.Contains(ai.lastRecent[1], "sata7 +1") {
		t.Fatalf("l'IA doit voir les deux événements, le plus ancien d'abord: %v", ai.lastRecent)
	}
	if len(s.msgs) != 0 {
		t.Fatal("un événement isolé reste une information du résumé")
	}
	d.digest(time.Date(2026, 10, 26, 13, 0, 0, 0, time.Local))
	html := s.msgs[0].HTML
	if !strings.Contains(html, "Événements des dernières 24 h") || !strings.Contains(html, "08:08") || !strings.Contains(html, "09:24") {
		t.Fatalf("le résumé doit lister les événements des dernières 24 h: %s", s.msgs[0].Subject)
	}
}

func TestAIRequestIncludesRecentEvents(t *testing.T) {
	var body map[string]any
	a, _ := aiServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		io.WriteString(w, apiOK(goodAnswer))
	})
	_, err := a.analyze(context.Background(), "alerte", []card{sampleCard("critique")}, nil, nil, []string{"26/10 08:08 — sata1 +1 (basse)"})
	if err != nil {
		t.Fatal(err)
	}
	user := body["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(user, "evenements_recents") || !strings.Contains(user, "sata1 +1") {
		t.Fatalf("les événements récents doivent figurer dans la requête: %s", user)
	}
	if !strings.Contains(body["system"].(string), "imprimante 3D") {
		t.Fatal("le contexte de l'installation (imprimante 3D dans le meuble) doit être dans les consignes")
	}
}

func TestKnownFileIsReloadedWithoutRestart(t *testing.T) {
	path := t.TempDir() + "/connus.txt"
	if err := os.WriteFile(path, []byte("# vide\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, s := newTestDispatcher(nil, nil)
	d.knownPath = path
	fan := item("fresh", "corsair-fan-15", levelWarn, "ventilateur du meuble canal 15 arrêté (0 tr/min)")

	d.process([]alertItem{fan})
	if len(s.msgs) != 1 {
		t.Fatalf("avant la déclaration, l'alerte part: %d message(s)", len(s.msgs))
	}
	// L'utilisateur déclare la situation: aucun relancement.
	if err := os.WriteFile(path, []byte("corsair-fan-15 | débranché volontairement (porte retirée)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d.process([]alertItem{fan})
	if len(s.msgs) != 1 {
		t.Fatalf("après la déclaration, plus aucune alerte: %d message(s)", len(s.msgs))
	}
	// Fichier devenu illisible: la dernière table connue est gardée (pas de réveil d'alertes en cascade).
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	d.process([]alertItem{fan})
	if len(s.msgs) != 1 {
		t.Fatalf("un fichier illisible ne doit pas vider la table: %d message(s)", len(s.msgs))
	}
	// Le fichier réapparaît sans la ligne: l'alerte repart.
	if err := os.WriteFile(path, []byte("# plus rien\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d.process([]alertItem{fan})
	if len(s.msgs) != 2 {
		t.Fatalf("la ligne retirée du fichier remet l'alerte en service: %d message(s)", len(s.msgs))
	}
}

func TestParseKnown(t *testing.T) {
	k := parseKnown("# commentaire\ndisk-bad-sata2 | remplacement prévu\r\n\n  disk-errors-sata4|  à tester \nsans-barre\n")
	if k["disk-bad-sata2"] != "remplacement prévu" || k["disk-errors-sata4"] != "à tester" {
		t.Fatalf("%v", k)
	}
	if _, ok := k["sans-barre"]; !ok {
		t.Fatal("une clé sans note est acceptée")
	}
}

package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- client du Bridge (faux Bridge HTTPS) ----

type fakeBridge struct {
	mu    sync.Mutex
	on    bool
	link  string
	puts  []string
	code  int    // code HTTP forcé (0 = normal)
	putEr string // erreur renvoyée dans la réponse du PUT
}

func (b *fakeBridge) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if r.Header.Get("hue-application-key") != "CLE-DE-TEST" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if b.code != 0 {
			w.WriteHeader(b.code)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/clip/v2/resource/device":
			io.WriteString(w, `{"data":[
{"metadata":{"name":"Lampe salon"},"services":[{"rid":"L0","rtype":"light"}]},
{"metadata":{"name":"Aération NAS"},"services":[{"rid":"L1","rtype":"light"},{"rid":"Z1","rtype":"zigbee_connectivity"}]}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/clip/v2/resource/light/L1":
			json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"on": map[string]bool{"on": b.on}}}})
		case r.Method == http.MethodGet && r.URL.Path == "/clip/v2/resource/zigbee_connectivity/Z1":
			json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"status": b.link}}})
		case r.Method == http.MethodPut && r.URL.Path == "/clip/v2/resource/light/L1":
			body, _ := io.ReadAll(r.Body)
			b.puts = append(b.puts, string(body))
			if b.putEr != "" {
				json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]string{"description": b.putEr}}})
				return
			}
			var v struct {
				On struct {
					On bool `json:"on"`
				} `json:"on"`
			}
			json.Unmarshal(body, &v)
			b.on = v.On.On
			io.WriteString(w, `{"data":[{"rid":"L1","rtype":"light"}],"errors":[]}`)
		default:
			t.Errorf("requête inattendue: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func newFakeHue(t *testing.T, name string) (*hueClient, *fakeBridge, func()) {
	t.Helper()
	b := &fakeBridge{on: true, link: "connectivity_issue"}
	srv := httptest.NewTLSServer(b.handler(t))
	c := newHueClient(strings.TrimPrefix(srv.URL, "https://"), "CLE-DE-TEST", name)
	return c, b, srv.Close
}

func TestHueClientLitEtCommandeLaPrise(t *testing.T) {
	c, b, stop := newFakeHue(t, "Aération NAS")
	defer stop()
	on, link, err := c.Read()
	if err != nil || !on || link != "connectivity_issue" {
		t.Fatalf("lecture: on=%v link=%q err=%v", on, link, err)
	}
	if err := c.Set(false); err != nil {
		t.Fatal(err)
	}
	if len(b.puts) != 1 || !strings.Contains(b.puts[0], `"on":{"on":false}`) {
		t.Fatalf("ordre OFF attendu: %v", b.puts)
	}
	if on, _, _ := c.Read(); on {
		t.Error("la prise doit être lue éteinte après l'ordre")
	}
	if err := c.Set(true); err != nil || !b.on {
		t.Fatalf("ordre ON: %v", err)
	}
}

func TestHueClientErreurs(t *testing.T) {
	if newHueClient("", "k", "p") != nil || newHueClient("1.2.3.4", "", "p") != nil || newHueClient("1.2.3.4", "k", "") != nil {
		t.Error("adresse, clé et nom sont tous nécessaires")
	}
	c, _, stop := newFakeHue(t, "Prise inconnue")
	if _, _, err := c.Read(); err == nil || !strings.Contains(err.Error(), "introuvable") {
		t.Errorf("nom inconnu: erreur « introuvable » attendue: %v", err)
	}
	stop()

	c, b, stop := newFakeHue(t, "Aération NAS")
	defer stop()
	b.code = http.StatusInternalServerError
	if _, _, err := c.Read(); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("code HTTP 500: %v", err)
	}
	b.code, b.putEr = 0, "device (light) has communication issues"
	if err := c.Set(true); err == nil || !strings.Contains(err.Error(), "communication issues") {
		t.Errorf("erreur du Bridge à rapporter: %v", err)
	}
	bad := newHueClient(strings.TrimPrefix("https://127.0.0.1:1", "https://"), "k", "Aération NAS")
	if _, _, err := bad.Read(); err == nil {
		t.Error("Bridge injoignable: erreur attendue")
	}
}

func TestHueClientMauvaiseCleRefusee(t *testing.T) {
	c, _, stop := newFakeHue(t, "Aération NAS")
	defer stop()
	c.key = "MAUVAISE"
	if _, _, err := c.Read(); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("clé refusée: code 403 attendu: %v", err)
	}
}

// ---- lecture, suite d'échecs ----

type fakePlug struct {
	mu       sync.Mutex
	on       bool
	link     string
	calls    []string
	readErr  error
	setErrs  []error // erreurs successives de Set (consommées dans l'ordre)
	alwaysEr error
}

func (f *fakePlug) Read() (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "read")
	if f.readErr != nil {
		return false, "", f.readErr
	}
	return f.on, f.link, nil
}

func (f *fakePlug) Set(on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if on {
		f.calls = append(f.calls, "on")
	} else {
		f.calls = append(f.calls, "off")
	}
	if f.alwaysEr != nil && on {
		return f.alwaysEr
	}
	if len(f.setErrs) > 0 {
		e := f.setErrs[0]
		f.setErrs = f.setErrs[1:]
		if e != nil {
			return e
		}
	}
	f.on = on
	return nil
}

func (f *fakePlug) log() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, ",")
}

func TestReadPlugSuiteDEchecs(t *testing.T) {
	p := &fakePlug{on: true, link: "connected"}
	st := readPlug(p, plugState{}, at(100))
	if !st.Known || !st.On || st.Link != "connected" || !st.ErrSince.IsZero() {
		t.Fatalf("lecture réussie: %+v", st)
	}
	p.readErr = errors.New("délai dépassé")
	st = readPlug(p, st, at(160))
	if !st.Known || !st.On || st.ErrSince != at(160) || st.LastErr != "délai dépassé" {
		t.Fatalf("1er échec: la dernière valeur connue est gardée, la série d'échecs commence: %+v", st)
	}
	st = readPlug(p, st, at(220))
	if st.ErrSince != at(160) {
		t.Errorf("la série d'échecs garde son début: %+v", st)
	}
	p.readErr = nil
	st = readPlug(p, st, at(300))
	if !st.ErrSince.IsZero() || st.LastErr != "" {
		t.Errorf("lecture revenue: série d'échecs effacée: %+v", st)
	}
}

// ---- exécution des ordres ----

func noSleep(t *testing.T) {
	old := hueSleep
	hueSleep = func(time.Duration) {}
	t.Cleanup(func() { hueSleep = old })
}

func TestHueRunOnEtCycle(t *testing.T) {
	noSleep(t)
	p := &fakePlug{on: false, link: "connected"}
	st, err := hueRun(p, "on")
	if err != nil || !st.On || p.log() != "on,read" {
		t.Fatalf("ordre ON: %v %+v %s", err, st, p.log())
	}
	p = &fakePlug{on: true, link: "connected"}
	st, err = hueRun(p, "cycle")
	if err != nil || !st.On || p.log() != "off,on,read" {
		t.Fatalf("cycle: couper, rallumer, relire: %v %+v %s", err, st, p.log())
	}
	if _, err := hueRun(p, "autre"); err == nil {
		t.Error("ordre inconnu refusé")
	}
}

func TestHueRunCycleReessaieLeRallumage(t *testing.T) {
	noSleep(t)
	p := &fakePlug{on: true, setErrs: []error{nil, errors.New("zigbee"), errors.New("zigbee")}} // OFF ok, puis 2 échecs de ON
	st, err := hueRun(p, "cycle")
	if err != nil || !st.On || p.log() != "off,on,on,on,read" {
		t.Fatalf("le rallumage doit être réessayé: %v %s", err, p.log())
	}
	p = &fakePlug{on: true, alwaysEr: errors.New("hors de portée")}
	if _, err := hueRun(p, "cycle"); err == nil || !strings.Contains(err.Error(), "impossible de la rallumer") {
		t.Errorf("échec définitif attendu: %v", err)
	}
	if strings.Count(p.log(), "on") != 5 {
		t.Errorf("5 essais de rallumage attendus: %s", p.log())
	}
	p = &fakePlug{on: true, setErrs: []error{errors.New("refusé")}}
	if _, err := hueRun(p, "cycle"); err == nil || p.log() != "off" {
		t.Errorf("coupure refusée: rien d'autre n'est envoyé: %v %s", err, p.log())
	}
}

func TestHueMessageSelonLeResultat(t *testing.T) {
	ok := hueMessage("on", "prise éteinte", plugState{Known: true, On: true}, nil, false, "http://x/")
	if ok.Priority != 5 || !strings.Contains(ok.Short, "confirme") || !strings.Contains(ok.Subject, "rallumée") {
		t.Errorf("succès: %+v", ok)
	}
	cyc := hueMessage("cycle", "Hub absent", plugState{Known: true, On: true}, nil, false, "")
	if !strings.Contains(cyc.Subject, "coupée puis rallumée") || !strings.Contains(cyc.Body, "OpenLinkHub est relancé") {
		t.Errorf("cycle: %+v", cyc)
	}
	ko := hueMessage("on", "x", plugState{}, errors.New("hors de portée"), false, "")
	if !strings.Contains(ko.Short, "ÉCHEC") || ko.Tags[0] != "rotating_light" {
		t.Errorf("échec: %+v", ko)
	}
	dry := hueMessage("on", "x", plugState{}, nil, true, "")
	if dry.Priority != 3 || !strings.Contains(dry.Short, "mode à blanc") {
		t.Errorf("mode à blanc: %+v", dry)
	}
}

// ---- orchestration: décision, exécution en arrière-plan, état partagé ----

func newTestCtl(t *testing.T, p *fakePlug, dry bool) (*hueCtl, *shared, *[]message, *sync.Mutex) {
	t.Helper()
	noSleep(t)
	st := &shared{}
	var mu sync.Mutex
	var sent []message
	c := newHueCtl(p, newHueGuard(defaultHueCfg()), st, func(m message) { mu.Lock(); sent = append(sent, m); mu.Unlock() }, nil, dry, nil, "")
	c.logger = func(string, ...any) {}
	return c, st, &sent, &mu
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition non atteinte à temps")
}

func hueAssess(st *shared, now time.Time) assessInput {
	in, _ := st.view(now)
	in.Enforce, in.NasCh, in.QnapCh = true, 13, 14
	in.Devs, in.DevsOK = hueBase(now).Devs, true
	return in
}

func TestHueCtlRallumeUnePriseEteinteEtNotifie(t *testing.T) {
	p := &fakePlug{on: false, link: "connected"}
	c, st, sent, mu := newTestCtl(t, p, false)
	c.refresh() // lit « éteinte »
	now := time.Now()
	if r := c.step(hueAssess(st, now), now, ventOK, time.Time{}); r != "" {
		t.Fatalf("pas d'arrêt: %s", r)
	}
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(*sent) == 1 })
	if !p.on || p.log() != "read,on,read" {
		t.Errorf("la prise doit être rallumée puis relue: on=%v %s", p.on, p.log())
	}
	if (*sent)[0].Priority != 5 || !strings.Contains((*sent)[0].Short, "confirme") {
		t.Errorf("notification attendue: %+v", (*sent)[0])
	}
	if in, _ := st.view(time.Now()); !in.Hue.State.On || in.Hue.LastKind != "on" {
		t.Errorf("état partagé à jour (prise allumée, dernier ordre « on »): %+v", in.Hue)
	}
}

func TestHueCtlModeABlancNEnvoieAucunOrdre(t *testing.T) {
	p := &fakePlug{on: false, link: "connected"}
	c, st, sent, mu := newTestCtl(t, p, true)
	c.refresh()
	now := time.Now()
	c.step(hueAssess(st, now), now, ventOK, time.Time{})
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(*sent) == 1 })
	if p.on || strings.Contains(p.log(), "on") {
		t.Errorf("mode à blanc: aucun ordre à la prise: %s", p.log())
	}
	if !strings.Contains((*sent)[0].Short, "mode à blanc") {
		t.Errorf("la notification le dit: %+v", (*sent)[0])
	}
}

func TestHueCtlRaisonDArretRenvoyee(t *testing.T) {
	p := &fakePlug{on: false, link: "connected", alwaysEr: errors.New("hors de portée")}
	c, st, _, _ := newTestCtl(t, p, false)
	c.refresh()
	now := time.Now()
	c.step(hueAssess(st, now), now, ventOK, time.Time{})
	later := now.Add(5*time.Minute + time.Second)
	in := hueAssess(st, later)
	if r := c.step(in, later, ventOK, time.Time{}); !strings.Contains(r, "éteinte depuis") {
		t.Fatalf("arrêt attendu quand la prise reste éteinte 5 minutes: %q", r)
	}
}

func TestHueCtlPriseInjoignablePuisRevenue(t *testing.T) {
	p := &fakePlug{on: true, link: "connected", readErr: errors.New("délai dépassé")}
	c, st, _, _ := newTestCtl(t, p, false)
	var logs []string
	c.logger = func(f string, a ...any) { logs = append(logs, f) }
	c.refresh()
	in, _ := st.view(time.Now())
	if in.Hue.State.Known || in.Hue.State.ErrSince.IsZero() {
		t.Fatalf("Bridge injoignable dès la 1re lecture: %+v", in.Hue.State)
	}
	p.readErr = nil
	c.refresh()
	in, _ = st.view(time.Now())
	if !in.Hue.State.Known || !in.Hue.State.ErrSince.IsZero() {
		t.Errorf("lecture revenue: %+v", in.Hue.State)
	}
	if len(logs) < 2 {
		t.Errorf("l'indisponibilité et le retour doivent être journalisés: %v", logs)
	}
}

// ---- alertes et tuile ----

func TestHueFindingsEtTuile(t *testing.T) {
	lim := defaultLimits()
	now := at(10000)
	off := hueView{Enabled: true, Now: now, State: plugState{Known: true, On: false, Link: "connected"}}
	f := hueFindings(off, true, false, lim)
	if len(f) != 1 || f[0].Key != "hue-off" || f[0].Level != levelCrit {
		t.Fatalf("prise éteinte: alerte critique attendue: %+v", f)
	}
	if f := hueFindings(off, true, true, lim); len(f) != 0 {
		t.Errorf("maintenance: aucune alerte: %+v", f)
	}
	if f := hueFindings(off, false, false, lim); len(f) != 0 {
		t.Errorf("délai de grâce: aucune alerte: %+v", f)
	}
	if f := hueFindings(hueView{}, true, false, lim); len(f) != 0 {
		t.Errorf("Hue non configuré: aucune alerte: %+v", f)
	}
	down := hueView{Enabled: true, Now: now, State: plugState{Known: true, On: true, ErrSince: at(10000 - 11*60), LastErr: "délai dépassé"}}
	if f := hueFindings(down, true, false, lim); len(f) != 1 || f[0].Key != "hue-unreachable" {
		t.Errorf("Bridge muet depuis 11 minutes: alerte attendue: %+v", f)
	}
	recent := hueView{Enabled: true, Now: now, State: plugState{Known: true, On: true, ErrSince: at(10000 - 3*60)}}
	if f := hueFindings(recent, true, false, lim); len(f) != 0 {
		t.Errorf("3 minutes seulement: pas encore d'alerte: %+v", f)
	}

	tl, ok := hueTile(hueView{Enabled: true, Now: now, State: plugState{Known: true, On: true, Link: "connectivity_issue"}, LastOrder: at(9000), LastKind: "cycle"})
	if !ok || tl.Value != "allumée" || tl.Level != "ok" || !strings.Contains(tl.Sub, "instable") || !strings.Contains(tl.Sub2, "coupée 15 s") {
		t.Errorf("tuile prise allumée: %+v", tl)
	}
	tl, _ = hueTile(off)
	if tl.Value != "éteinte" || tl.Level != "crit" {
		t.Errorf("tuile prise éteinte: %+v", tl)
	}
	if _, ok := hueTile(hueView{}); ok {
		t.Error("pas de tuile sans Bridge configuré")
	}
}

func TestHueAnomalieDansAssessEtCarte(t *testing.T) {
	in := assessInput{Enforce: true, Hue: hueView{Enabled: true, Now: at(100), State: plugState{Known: true, On: false}}}
	var found bool
	for _, f := range assess(in, defaultLimits()) {
		if f.Key == "hue-off" {
			found = true
			c := describeFinding(f, in)
			if !strings.Contains(c.Title, "prise") || c.Gravity != "critique" || c.Action == "" {
				t.Errorf("carte de la prise éteinte: %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("assess doit signaler la prise éteinte")
	}
	c := describeFinding(finding{Key: "hue-unreachable", Level: levelWarn, Text: "le Bridge Hue ne répond plus"}, assessInput{})
	if !strings.Contains(c.Title, "Bridge") || c.Gravity != "moyenne" {
		t.Errorf("carte du Bridge injoignable: %+v", c)
	}
}

func TestHueCtlRelanceOpenLinkHubApresTousLesOrdresReussis(t *testing.T) {
	noSleep(t)
	for _, tc := range []struct {
		name   string
		action string
		dry    bool
		plug   *fakePlug
		want   int
	}{
		{"rallumage", "on", false, &fakePlug{on: false}, 1},
		{"cycle", "cycle", false, &fakePlug{on: true}, 1},
		{"mode à blanc", "on", true, &fakePlug{}, 0},
		{"ordre en échec", "on", false, &fakePlug{alwaysEr: errors.New("hors de portée")}, 0},
	} {
		calls := 0
		c := newHueCtl(tc.plug, newHueGuard(defaultHueCfg()), &shared{}, func(message) {}, nil, tc.dry, func() error { calls++; return nil }, "")
		c.logger = func(string, ...any) {}
		c.exec(hueDecision{Action: tc.action, Why: "essai"})
		if calls != tc.want {
			t.Errorf("%s: %d relance(s) d'OpenLinkHub, %d attendue(s)", tc.name, calls, tc.want)
		}
	}
}

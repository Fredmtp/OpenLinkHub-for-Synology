package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testPass = "mot-de-passe-test"

type ctlBench struct {
	c       *controlCtx
	srv     *httptest.Server
	beeps   atomic.Int32
	restart atomic.Int32
	maint   string
	silence string
}

func newCtlBench(t *testing.T, password string, withRestart bool) *ctlBench {
	b := &ctlBench{maint: filepath.Join(t.TempDir(), "maintenance"), silence: filepath.Join(t.TempDir(), "silence")}
	oldM, oldS := *maintFile, *silenceFile
	*maintFile, *silenceFile = b.maint, b.silence
	t.Cleanup(func() { *maintFile, *silenceFile = oldM, oldS })
	var restart func() error
	if withRestart {
		restart = func() error { b.restart.Add(1); return nil }
	}
	b.c = newControlCtx(password, func() { b.beeps.Add(1) }, alarmCfg{Gap: time.Millisecond, Count: 5}, restart, nil, true)
	b.c.delay = 0
	b.srv = httptest.NewServer(http.HandlerFunc(b.c.handle))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *ctlBench) req(t *testing.T, method, path string, pass string, form url.Values) *http.Response {
	t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r, _ := http.NewRequest(method, b.srv.URL+path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if pass != "" {
		r.SetBasicAuth("peu-importe", pass)
	}
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (b *ctlBench) act(t *testing.T, action string) *http.Response {
	return b.req(t, "POST", "/commandes", testPass, url.Values{"csrf": {b.c.csrf}, "action": {action}})
}

func TestControlsRequirePassword(t *testing.T) {
	b := newCtlBench(t, testPass, true)
	if r := b.req(t, "GET", "/commandes", "", nil); r.StatusCode != 401 || r.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("sans mot de passe: %d", r.StatusCode)
	}
	if r := b.req(t, "GET", "/commandes", "mauvais", nil); r.StatusCode != 401 {
		t.Fatalf("mauvais mot de passe: %d", r.StatusCode)
	}
	if r := b.req(t, "GET", "/commandes", testPass, nil); r.StatusCode != 200 {
		t.Fatalf("bon mot de passe: %d", r.StatusCode)
	}
}

func TestControlsDisabledWithoutAGoodPassword(t *testing.T) {
	for _, pw := range []string{"", "court"} {
		b := newCtlBench(t, pw, true)
		if r := b.req(t, "GET", "/commandes", pw, nil); r.StatusCode != 404 {
			t.Fatalf("mot de passe %q: page attendue désactivée, code %d", pw, r.StatusCode)
		}
		if b.c.enabled() {
			t.Fatalf("mot de passe %q: ne doit pas activer la page", pw)
		}
	}
	var nilCtl *controlCtx
	if nilCtl.enabled() {
		t.Fatal("un contrôleur absent n'est pas activé")
	}
}

func TestControlsLockoutAfterTooManyFailures(t *testing.T) {
	b := newCtlBench(t, testPass, true)
	for i := 0; i < controlLockN; i++ {
		b.req(t, "GET", "/commandes", "mauvais", nil)
	}
	if r := b.req(t, "GET", "/commandes", testPass, nil); r.StatusCode != 429 {
		t.Fatalf("même le bon mot de passe est refusé pendant le blocage: %d", r.StatusCode)
	}
	// le blocage se lève avec le temps
	b.c.mu.Lock()
	for i := range b.c.fails {
		b.c.fails[i] = time.Now().Add(-controlLockSpan - time.Second)
	}
	b.c.mu.Unlock()
	if r := b.req(t, "GET", "/commandes", testPass, nil); r.StatusCode != 200 {
		t.Fatalf("blocage levé: %d", r.StatusCode)
	}
}

func TestControlsPostNeedsTheToken(t *testing.T) {
	b := newCtlBench(t, testPass, true)
	if r := b.req(t, "POST", "/commandes", testPass, url.Values{"action": {"maintenance-on"}}); r.StatusCode != 403 {
		t.Fatalf("sans jeton: %d", r.StatusCode)
	}
	if r := b.req(t, "POST", "/commandes", testPass, url.Values{"csrf": {"faux"}, "action": {"maintenance-on"}}); r.StatusCode != 403 {
		t.Fatalf("mauvais jeton: %d", r.StatusCode)
	}
	if _, err := os.Stat(b.maint); err == nil {
		t.Fatal("aucune action sans jeton valide")
	}
	// le jeton est dans la page, visible seulement après authentification
	r := b.req(t, "GET", "/commandes", testPass, nil)
	buf := new(strings.Builder)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf.Write(tmp[:n])
		if err != nil {
			break
		}
	}
	if !strings.Contains(buf.String(), b.c.csrf) || !strings.Contains(buf.String(), "Maintenance 30 min") {
		t.Fatal("la page doit contenir le jeton et les boutons")
	}
}

func readEpoch(t *testing.T, path string) int64 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestControlsMaintenanceOnAndOff(t *testing.T) {
	b := newCtlBench(t, testPass, true)
	before := time.Now()
	if r := b.act(t, "maintenance-on"); r.StatusCode != 303 {
		t.Fatalf("code %d", r.StatusCode)
	}
	end := readEpoch(t, b.maint)
	if d := time.Unix(end, 0).Sub(before); d < 29*time.Minute || d > 31*time.Minute {
		t.Fatalf("durée de maintenance %s, attendu 30 min", d)
	}
	// la surveillance lit le même fichier: la maintenance est bien vue comme active, et plafonnée
	if _, on := maintenanceUntil(b.maint, time.Now()); !on {
		t.Fatal("la maintenance doit être active")
	}
	b.act(t, "maintenance-off")
	if _, err := os.Stat(b.maint); err == nil {
		t.Fatal("fin de maintenance: le fichier doit disparaître")
	}
	// fin sans maintenance en cours: pas d'erreur
	if r := b.act(t, "maintenance-off"); r.StatusCode != 303 || strings.Contains(r.Header.Get("Location"), "n=err") {
		t.Fatalf("fin répétée: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
}

func TestControlsSilenceOnAndOff(t *testing.T) {
	b := newCtlBench(t, testPass, true)
	b.act(t, "silence-on")
	if _, on := maintenanceUntil(b.silence, time.Now()); !on {
		t.Fatal("le silence doit être actif")
	}
	if _, err := os.Stat(b.maint); err == nil {
		t.Fatal("le silence ne touche pas à la maintenance")
	}
	b.act(t, "silence-off")
	if _, err := os.Stat(b.silence); err == nil {
		t.Fatal("fin du silence")
	}
}

func TestControlsBeepTestPlaysAFullSeriesAndIsRateLimited(t *testing.T) {
	b := newCtlBench(t, testPass, true)
	b.act(t, "test-bips")
	deadline := time.Now().Add(2 * time.Second)
	for b.beeps.Load() < 5 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := b.beeps.Load(); got != 5 {
		t.Fatalf("%d bips, attendu une série de 5", got)
	}
	r := b.act(t, "test-bips")
	if !strings.Contains(r.Header.Get("Location"), "n=err") {
		t.Fatalf("un second essai immédiat doit être refusé: %s", r.Header.Get("Location"))
	}
	time.Sleep(50 * time.Millisecond)
	if got := b.beeps.Load(); got != 5 {
		t.Fatalf("pas de nouvelle série: %d bips", got)
	}
}

func TestControlsRestartOnlyWhenConfigured(t *testing.T) {
	b := newCtlBench(t, testPass, true)
	b.act(t, "relancer")
	if b.restart.Load() != 1 {
		t.Fatalf("relance demandée une fois: %d", b.restart.Load())
	}
	b = newCtlBench(t, testPass, false)
	r := b.act(t, "relancer")
	if !strings.Contains(r.Header.Get("Location"), "n=err") || b.restart.Load() != 0 {
		t.Fatalf("sans -restart-cmd la relance est refusée: %s", r.Header.Get("Location"))
	}
}

func TestControlsUnknownActionAndMethod(t *testing.T) {
	b := newCtlBench(t, testPass, true)
	r := b.act(t, "reboot-du-nas")
	if !strings.Contains(r.Header.Get("Location"), "n=err") {
		t.Fatalf("action inconnue refusée: %s", r.Header.Get("Location"))
	}
	if r := b.req(t, "PUT", "/commandes", testPass, nil); r.StatusCode != 405 {
		t.Fatalf("méthode non autorisée: %d", r.StatusCode)
	}
}

func TestControlsFlashMessageIsEscaped(t *testing.T) {
	b := newCtlBench(t, testPass, true)
	r := b.req(t, "GET", "/commandes?m="+url.QueryEscape(`<script>alert(1)</script>`)+"&n=ok", testPass, nil)
	buf := make([]byte, 20000)
	n, _ := r.Body.Read(buf)
	if strings.Contains(string(buf[:n]), "<script>alert(1)</script>") {
		t.Fatal("le message affiché doit être échappé")
	}
}

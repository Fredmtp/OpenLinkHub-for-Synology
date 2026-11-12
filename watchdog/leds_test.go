package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fauxOLH imite l'API d'OpenLinkHub: un Hub avec trois extracteurs, une admission et deux sondes,
// plus une seconde entrée vide (null) comme la vraie réponse.
type fauxOLH struct {
	mu       sync.Mutex
	srv      *httptest.Server
	calls    []string        // « canal:profil » dans l'ordre des appels
	refuse   map[string]bool // profils refusés
	failNext map[string]bool // « canal:profil » qui échoue une fois
}

func newFauxOLH(t *testing.T) *fauxOLH {
	f := &fauxOLH{refuse: map[string]bool{}, failNext: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices/", func(w http.ResponseWriter, r *http.Request) {
		fan := func(ch int, prof string) string {
			return fmt.Sprintf(`"%d":{"channelId":%d,"description":"Fan","HasSpeed":true,"IsTemperatureProbe":false,"rgb":%q,"rpm":770}`, ch, ch, prof)
		}
		probe := `"13":{"channelId":13,"description":"Hub","HasSpeed":true,"IsTemperatureProbe":true,"rgb":"static","temperature":27}`
		fmt.Fprintf(w, `{"code":200,"devices":{"HUBID123":{"GetDevice":{"devices":{%s,%s,%s,%s,%s},`+
			`"DeviceProfile":{"Labels":{"1":"EXTRACTION#1","2":"EXTRACTION#3","3":"EXTRACTION#2","15":"Admission"}}}},"AUTRE":{"GetDevice":null}}}`,
			fan(1, "probe-temperature"), fan(2, "probe-temperature"), fan(3, "probe-temperature"), fan(15, "probe-temperature"), probe)
	})
	mux.HandleFunc("/api/color", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			DeviceID  string `json:"deviceId"`
			ChannelID int    `json:"channelId"`
			Profile   string `json:"profile"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		key := fmt.Sprintf("%d:%s", b.ChannelID, b.Profile)
		f.mu.Lock()
		f.calls = append(f.calls, key)
		bad := f.refuse[b.Profile] || f.failNext[key] || b.DeviceID != "HUBID123"
		delete(f.failNext, key)
		f.mu.Unlock()
		status := 1
		if bad {
			status = 0
		}
		fmt.Fprintf(w, `{"code":200,"status":%d,"message":"réponse"}`, status)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func TestFetchHubFans(t *testing.T) {
	f := newFauxOLH(t)
	hf, err := fetchHubFans(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if hf.HubID != "HUBID123" || len(hf.Fans) != 4 {
		t.Fatalf("%+v", hf)
	}
	want := []fanLED{{1, "EXTRACTION#1", "probe-temperature"}, {2, "EXTRACTION#3", "probe-temperature"},
		{3, "EXTRACTION#2", "probe-temperature"}, {15, "Admission", "probe-temperature"}}
	for i, w := range want {
		if hf.Fans[i] != w {
			t.Errorf("ventilateur %d: %+v, attendu %+v", i, hf.Fans[i], w)
		}
	}
}

func TestFetchHubFansEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":200,"devices":{"X":{"GetDevice":null}}}`)
	}))
	defer srv.Close()
	if _, err := fetchHubFans(srv.URL); err == nil {
		t.Fatal("aucun ventilateur: une erreur est attendue")
	}
}

func always(bool) func(time.Duration) bool { return func(time.Duration) bool { return true } }

func TestIdentifyLightsOneFanAtATimeAndRestoresEach(t *testing.T) {
	f := newFauxOLH(t)
	var out bytes.Buffer
	if err := identifyFans(f.srv.URL, "circle", 10*time.Second, &out, always(true)); err != nil {
		t.Fatal(err)
	}
	want := "1:circle 1:probe-temperature 2:circle 2:probe-temperature 3:circle 3:probe-temperature 15:circle 15:probe-temperature"
	if got := strings.Join(f.calls, " "); got != want {
		t.Fatalf("appels:\n %s\nattendu:\n %s", got, want)
	}
	if !strings.Contains(out.String(), "EXTRACTION#1") || !strings.Contains(out.String(), "tous les profils d'origine sont remis") {
		t.Fatalf("sortie: %s", out.String())
	}
}

func TestIdentifyInterruptionRestoresTheLitFan(t *testing.T) {
	f := newFauxOLH(t)
	var out bytes.Buffer
	n := 0
	wait := func(time.Duration) bool { n++; return n < 2 } // interrompu pendant le 2e ventilateur
	if err := identifyFans(f.srv.URL, "circle", time.Second, &out, wait); err != nil {
		t.Fatal(err)
	}
	want := "1:circle 1:probe-temperature 2:circle 2:probe-temperature"
	if got := strings.Join(f.calls, " "); got != want {
		t.Fatalf("appels: %s, attendu %s", got, want)
	}
	if !strings.Contains(out.String(), "Interrompu") {
		t.Fatalf("sortie: %s", out.String())
	}
}

func TestIdentifyUnknownProfileChangesNothing(t *testing.T) {
	f := newFauxOLH(t)
	f.refuse["inconnu"] = true
	var out bytes.Buffer
	err := identifyFans(f.srv.URL, "inconnu", time.Second, &out, always(true))
	if err == nil || !strings.Contains(err.Error(), "impossible d'appliquer") {
		t.Fatalf("erreur attendue: %v", err)
	}
	if got := strings.Join(f.calls, " "); got != "1:inconnu" {
		t.Fatalf("un seul essai attendu, puis arrêt: %s", got)
	}
}

func TestIdentifyReportsARestoreFailure(t *testing.T) {
	f := newFauxOLH(t)
	f.failNext["2:probe-temperature"] = true
	var out bytes.Buffer
	err := identifyFans(f.srv.URL, "circle", time.Second, &out, always(true))
	if err == nil || !strings.Contains(err.Error(), "à remettre à la main") {
		t.Fatalf("erreur attendue: %v", err)
	}
	if !strings.Contains(out.String(), "NON remis") || !strings.Contains(out.String(), "canal 2") {
		t.Fatalf("la sortie doit nommer le ventilateur non remis: %s", out.String())
	}
	// Les autres ventilateurs ont quand même été traités et remis.
	if !strings.Contains(strings.Join(f.calls, " "), "15:probe-temperature") {
		t.Fatalf("les ventilateurs suivants doivent être traités: %v", f.calls)
	}
}

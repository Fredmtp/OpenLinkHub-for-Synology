package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Pilotage des LED des ventilateurs par l'API d'OpenLinkHub. OpenLinkHub change l'effet lumineux d'un
// canal en lui donnant un PROFIL RGB par son nom (POST /api/color: deviceId du Hub, channelId, profile),
// par exemple « circle » ou « probe-temperature ». Seule la lumière change: pas la vitesse.
//
// Premier usage: l'identification des ventilateurs (-identify). Elle allume un ventilateur à la fois
// pour savoir lequel est à gauche, au milieu, à droite, puis remet TOUJOURS le profil d'origine.

type fanLED struct {
	Channel int
	Label   string // étiquette donnée dans OpenLinkHub (« EXTRACTION#1 »…)
	Profile string // profil RGB actuel (« probe-temperature »…)
}

type hubFans struct {
	HubID string
	Fans  []fanLED
}

// fetchHubFans lit les ventilateurs du Hub, avec leur profil RGB actuel et leur étiquette.
func fetchHubFans(base string) (hubFans, error) {
	var out hubFans
	resp, err := client.Get(base + "/api/devices/")
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r struct {
		Devices map[string]struct {
			GetDevice json.RawMessage `json:"GetDevice"`
		} `json:"devices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return out, err
	}
	for id, e := range r.Devices {
		var hub struct {
			Devices map[string]struct {
				ChannelID          int    `json:"channelId"`
				Description        string `json:"description"`
				HasSpeed           bool   `json:"HasSpeed"`
				IsTemperatureProbe bool   `json:"IsTemperatureProbe"`
				RGB                string `json:"rgb"`
			} `json:"devices"`
			DeviceProfile struct {
				Labels map[string]string `json:"Labels"`
			} `json:"DeviceProfile"`
		}
		if json.Unmarshal(e.GetDevice, &hub) != nil {
			continue
		}
		for _, d := range hub.Devices {
			if d.HasSpeed && !d.IsTemperatureProbe && d.Description == "Fan" {
				out.HubID = id
				out.Fans = append(out.Fans, fanLED{Channel: d.ChannelID, Label: hub.DeviceProfile.Labels[strconv.Itoa(d.ChannelID)], Profile: d.RGB})
			}
		}
	}
	if out.HubID == "" || len(out.Fans) == 0 {
		return out, fmt.Errorf("aucun ventilateur trouvé dans la réponse d'OpenLinkHub")
	}
	sort.Slice(out.Fans, func(i, j int) bool { return out.Fans[i].Channel < out.Fans[j].Channel })
	return out, nil
}

// setFanProfile donne un profil RGB à un canal du Hub.
func setFanProfile(base, hubID string, channel int, profile string) error {
	body, _ := json.Marshal(map[string]any{"deviceId": hubID, "channelId": channel, "profile": profile})
	resp, err := client.Post(base+"/api/color", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r struct {
		Status  int    `json:"status"`
		Message string `json:"message"`
	}
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("réponse illisible: %v", err)
	}
	if r.Status != 1 {
		return fmt.Errorf("refusé par OpenLinkHub: %s", r.Message)
	}
	return nil
}

// identifyFans allume un ventilateur à la fois avec le profil donné, pendant `hold`, puis lui remet son
// profil d'origine. wait renvoie faux quand l'opération est interrompue; le ventilateur allumé à ce
// moment-là est alors remis en l'état avant de rendre la main. Renvoie une erreur si un profil n'a pas
// pu être appliqué ou remis.
func identifyFans(base, profile string, hold time.Duration, out io.Writer, wait func(time.Duration) bool) error {
	hf, err := fetchHubFans(base)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Ventilateurs trouvés (profil de lumière actuel):\n")
	for _, f := range hf.Fans {
		fmt.Fprintf(out, "  canal %d  étiquette %q  profil %q\n", f.Channel, f.Label, f.Profile)
	}
	fmt.Fprintf(out, "\nChaque ventilateur va s'allumer en « %s » pendant %s, un à la fois. Regardez lequel s'allume.\n\n", profile, hold.Round(time.Second))

	var failed []string
	for _, f := range hf.Fans {
		fmt.Fprintf(out, "→ canal %d (%s) allumé maintenant…\n", f.Channel, orDash(f.Label))
		if err := setFanProfile(base, hf.HubID, f.Channel, profile); err != nil {
			return fmt.Errorf("canal %d: impossible d'appliquer le profil %q: %v", f.Channel, profile, err)
		}
		keepGoing := wait(hold)
		if err := setFanProfile(base, hf.HubID, f.Channel, f.Profile); err != nil {
			failed = append(failed, fmt.Sprintf("canal %d (profil d'origine %q): %v", f.Channel, f.Profile, err))
		}
		if !keepGoing {
			fmt.Fprintln(out, "Interrompu: le ventilateur allumé a été remis en l'état.")
			break
		}
	}
	if len(failed) > 0 {
		for _, s := range failed {
			fmt.Fprintf(out, "ATTENTION, profil d'origine NON remis: %s\n", s)
		}
		return fmt.Errorf("%d ventilateur(s) à remettre à la main dans OpenLinkHub", len(failed))
	}
	fmt.Fprintln(out, "\nTerminé: tous les profils d'origine sont remis.")
	return nil
}

// parseChannels lit une liste de canaux séparés par des virgules (« 1,3,2 »).
func parseChannels(s string) ([]int, error) {
	var out []int
	for _, p := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("canal %q invalide", p)
		}
		out = append(out, n)
	}
	return out, nil
}

// demoAlternate joue l'alternance « NAS qui chauffe »: les ventilateurs d'extraction s'allument UN À LA
// FOIS, dans l'ordre donné (de gauche à droite), à plusieurs rythmes, pour que l'utilisateur choisisse.
// À chaque pas, le ventilateur précédent reprend son profil d'origine. Tout est remis en l'état à la fin
// et en cas d'interruption. Chaque changement de profil est écrit sur disque par OpenLinkHub: l'outil
// mesure aussi le temps de réponse de l'API, pour savoir si un rythme rapide est tenable.
func demoAlternate(base, profile string, order []int, periods []time.Duration, tours int, out io.Writer, wait func(time.Duration) bool) error {
	hf, err := fetchHubFans(base)
	if err != nil {
		return err
	}
	orig := map[int]string{}
	for _, f := range hf.Fans {
		orig[f.Channel] = f.Profile
	}
	for _, ch := range order {
		if _, ok := orig[ch]; !ok {
			return fmt.Errorf("le canal %d n'est pas un ventilateur du Hub", ch)
		}
	}
	var failed []string
	lit := 0
	restore := func() {
		if lit == 0 {
			return
		}
		if err := setFanProfile(base, hf.HubID, lit, orig[lit]); err != nil {
			failed = append(failed, fmt.Sprintf("canal %d (profil d'origine %q): %v", lit, orig[lit], err))
		}
		lit = 0
	}
	fmt.Fprintf(out, "Alternance des extracteurs en « %s », de gauche à droite: canaux %v. %d tours par rythme.\n", profile, order, tours)
	interrupted := false
	for _, period := range periods {
		fmt.Fprintf(out, "\n=== Rythme: un ventilateur toutes les %s ===\n", period)
		var slowest time.Duration
		for tour := 0; tour < tours && !interrupted; tour++ {
			for _, ch := range order {
				restore()
				start := time.Now()
				if err := setFanProfile(base, hf.HubID, ch, profile); err != nil {
					restore()
					return fmt.Errorf("canal %d: impossible d'appliquer le profil %q: %v", ch, profile, err)
				}
				if d := time.Since(start); d > slowest {
					slowest = d
				}
				lit = ch
				if !wait(period) {
					interrupted = true
					break
				}
			}
		}
		restore()
		fmt.Fprintf(out, "Réponse d'OpenLinkHub la plus lente sur ce rythme: %s\n", slowest.Round(time.Millisecond))
		if interrupted {
			fmt.Fprintln(out, "Interrompu: tout est remis en l'état.")
			break
		}
		if !wait(3 * time.Second) {
			break
		}
	}
	restore()
	if len(failed) > 0 {
		for _, s := range failed {
			fmt.Fprintf(out, "ATTENTION, profil d'origine NON remis: %s\n", s)
		}
		return fmt.Errorf("%d ventilateur(s) à remettre à la main dans OpenLinkHub", len(failed))
	}
	fmt.Fprintln(out, "\nTerminé: tous les profils d'origine sont remis.")
	return nil
}

// runDemoAlternate est le point d'entrée de l'option -demo-alternate.
func runDemoAlternate(base, profile, order string, tours int) int {
	chans, err := parseChannels(order)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERREUR: %v\n", err)
		return 1
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	wait := func(d time.Duration) bool {
		select {
		case <-time.After(d):
			return true
		case <-sig:
			return false
		}
	}
	periods := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	if err := demoAlternate(base, profile, chans, periods, tours, os.Stdout, wait); err != nil {
		fmt.Fprintf(os.Stderr, "ERREUR: %v\n", err)
		return 1
	}
	return 0
}

// runIdentify est le point d'entrée de l'option -identify. Une interruption (Ctrl+C, arrêt) remet le
// ventilateur allumé à son profil d'origine avant de quitter.
func runIdentify(base, profile string, seconds int) int {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	wait := func(d time.Duration) bool {
		select {
		case <-time.After(d):
			return true
		case <-sig:
			return false
		}
	}
	if err := identifyFans(base, profile, time.Duration(seconds)*time.Second, os.Stdout, wait); err != nil {
		fmt.Fprintf(os.Stderr, "ERREUR: %v\n", err)
		return 1
	}
	return 0
}

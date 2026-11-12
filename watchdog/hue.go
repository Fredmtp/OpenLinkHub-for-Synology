package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Prise Philips Hue (« Aération NAS ») qui alimente les ventilateurs du meuble. La surveillance la lit et la
// commande par l'API locale du Bridge (v2, HTTPS), sans passer par Internet. La clé d'accès et l'adresse du
// Bridge viennent de secrets.env (HUE_KEY, HUE_BRIDGE); elles ne sont jamais écrites dans le journal.
//
// Le Bridge a un certificat auto-signé: la vérification est désactivée pour CE client seulement, qui ne parle
// qu'à l'adresse du Bridge configurée, sur le réseau local.

// plugState est la dernière lecture de la prise.
type plugState struct {
	Known    bool      // au moins une lecture réussie
	On       bool      // dernière valeur lue (état commandé: la prise ne mesure pas la consommation)
	Link     string    // état de la liaison Zigbee vu par le Bridge (connected, connectivity_issue, …)
	At       time.Time // date de la dernière lecture réussie
	ErrSince time.Time // début de la série d'échecs de lecture en cours (zéro = la dernière lecture a réussi)
	LastErr  string
}

// plugDevice est ce dont la surveillance a besoin: lire l'état et allumer ou éteindre.
type plugDevice interface {
	Read() (on bool, link string, err error)
	Set(on bool) error
}

type hueClient struct {
	base     string // https://<adresse du Bridge>
	key      string
	plugName string
	http     *http.Client
	lightID  string
	connID   string
}

func newHueClient(bridge, key, plugName string) *hueClient {
	bridge = strings.TrimSpace(bridge)
	if bridge == "" || strings.TrimSpace(key) == "" || strings.TrimSpace(plugName) == "" {
		return nil
	}
	return &hueClient{
		base: "https://" + bridge, key: strings.TrimSpace(key), plugName: strings.TrimSpace(plugName),
		http: &http.Client{
			Timeout:   8 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // certificat auto-signé du Bridge
		},
	}
}

func (h *hueClient) do(method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("hue-application-key", h.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Bridge Hue: code HTTP %d", resp.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("réponse du Bridge illisible: %v", err)
		}
	}
	return nil
}

type hueService struct {
	RID   string `json:"rid"`
	RType string `json:"rtype"`
}

// resolve retrouve les identifiants de la prise d'après son nom (une seule fois, puis en mémoire).
func (h *hueClient) resolve() error {
	if h.lightID != "" {
		return nil
	}
	var devs struct {
		Data []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Services []hueService `json:"services"`
		} `json:"data"`
	}
	if err := h.do(http.MethodGet, "/clip/v2/resource/device", nil, &devs); err != nil {
		return err
	}
	for _, d := range devs.Data {
		if d.Metadata.Name != h.plugName {
			continue
		}
		for _, s := range d.Services {
			switch s.RType {
			case "light":
				h.lightID = s.RID
			case "zigbee_connectivity":
				h.connID = s.RID
			}
		}
		if h.lightID != "" {
			return nil
		}
	}
	return fmt.Errorf("prise « %s » introuvable sur le Bridge Hue", h.plugName)
}

func (h *hueClient) Read() (bool, string, error) {
	if err := h.resolve(); err != nil {
		return false, "", err
	}
	var l struct {
		Data []struct {
			On struct {
				On bool `json:"on"`
			} `json:"on"`
		} `json:"data"`
	}
	if err := h.do(http.MethodGet, "/clip/v2/resource/light/"+h.lightID, nil, &l); err != nil {
		return false, "", err
	}
	if len(l.Data) == 0 {
		return false, "", errors.New("Bridge Hue: prise absente de la réponse")
	}
	link := ""
	if h.connID != "" {
		var z struct {
			Data []struct {
				Status string `json:"status"`
			} `json:"data"`
		}
		if err := h.do(http.MethodGet, "/clip/v2/resource/zigbee_connectivity/"+h.connID, nil, &z); err == nil && len(z.Data) > 0 {
			link = z.Data[0].Status
		}
	}
	return l.Data[0].On.On, link, nil
}

func (h *hueClient) Set(on bool) error {
	if err := h.resolve(); err != nil {
		return err
	}
	body := map[string]any{"on": map[string]bool{"on": on}}
	var out struct {
		Errors []struct {
			Description string `json:"description"`
		} `json:"errors"`
	}
	if err := h.do(http.MethodPut, "/clip/v2/resource/light/"+h.lightID, body, &out); err != nil {
		return err
	}
	if len(out.Errors) > 0 {
		return fmt.Errorf("Bridge Hue: %s", out.Errors[0].Description)
	}
	return nil
}

// readPlug lit la prise et met à jour l'état mémorisé (suite d'échecs comprise). Fonction pure côté état.
func readPlug(p plugDevice, prev plugState, now time.Time) plugState {
	on, link, err := p.Read()
	if err != nil {
		prev.LastErr = err.Error()
		if prev.ErrSince.IsZero() {
			prev.ErrSince = now
		}
		return prev
	}
	return plugState{Known: true, On: on, Link: link, At: now}
}

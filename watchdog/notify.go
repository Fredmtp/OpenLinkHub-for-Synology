package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Notifications: ntfy (texte court sur le téléphone) et mail Microsoft Graph (détails).
// Rien ici ne doit pouvoir bloquer ou faire échouer la surveillance: les envois passent par une file
// traitée dans une goroutine, avec des délais maximaux.

type message struct {
	Items    []alertItem // événements structurés (traités par le répartiteur); vide pour un message simple
	HTML     string      // version mise en page du mail; sans elle, le mail part en texte
	Subject  string
	Body     string // texte détaillé (mail)
	Short    string // texte court (ntfy): ni adresse, ni nom d'hôte, ni numéro de série
	Priority int    // 1 à 5, échelle ntfy
	Tags     []string
}

type sender interface {
	Send(m message) error
	Name() string
}

// parseEnv lit un fichier « CLE=valeur » (commentaires # et guillemets tolérés).
func parseEnv(txt string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(txt, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, "=")
		if i <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(line[i+1:])
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

// ---- ntfy ----

type ntfySender struct {
	server, topic, token string
	hc                   *http.Client
}

func (n *ntfySender) Name() string { return "ntfy" }

func (n *ntfySender) Send(m message) error {
	body := map[string]any{"topic": n.topic, "title": "NAS meuble", "message": m.Short,
		"priority": m.Priority, "tags": m.Tags}
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(n.server, "/")+"/", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	}
	resp, err := n.hc.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy: %w", errors.New(scrub(err.Error(), n.topic, n.token)))
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy: HTTP %d", resp.StatusCode)
	}
	return nil
}

// ---- Microsoft Graph ----

type graphSender struct {
	tenant, clientID, secret string
	from                     string
	to                       []string
	hc                       *http.Client

	mu     sync.Mutex
	token  string
	expiry time.Time
}

func (g *graphSender) Name() string { return "mail" }

func (g *graphSender) accessToken() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token != "" && time.Now().Before(g.expiry.Add(-time.Minute)) {
		return g.token, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {g.clientID},
		"client_secret": {g.secret},
		"scope":         {"https://graph.microsoft.com/.default"},
	}
	resp, err := g.hc.PostForm("https://login.microsoftonline.com/"+url.PathEscape(g.tenant)+"/oauth2/v2.0/token", form)
	if err != nil {
		return "", fmt.Errorf("graph: jeton: %s", scrub(err.Error(), g.secret, g.clientID))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("graph: jeton refusé (HTTP %d, %s)", resp.StatusCode, graphErr(raw))
	}
	var t struct {
		Access    string  `json:"access_token"`
		ExpiresIn float64 `json:"expires_in"`
	}
	if json.Unmarshal(raw, &t) != nil || t.Access == "" {
		return "", errors.New("graph: réponse de jeton illisible")
	}
	g.token = t.Access
	g.expiry = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	return g.token, nil
}

func (g *graphSender) Send(m message) error {
	tok, err := g.accessToken()
	if err != nil {
		return err
	}
	var rcpt []map[string]any
	for _, a := range g.to {
		rcpt = append(rcpt, map[string]any{"emailAddress": map[string]string{"address": a}})
	}
	body := map[string]string{"contentType": "Text", "content": m.Body}
	if m.HTML != "" {
		body = map[string]string{"contentType": "HTML", "content": m.HTML}
	}
	payload := map[string]any{
		"message": map[string]any{
			"subject":      m.Subject,
			"body":         body,
			"toRecipients": rcpt,
		},
		"saveToSentItems": false,
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost,
		"https://graph.microsoft.com/v1.0/users/"+url.PathEscape(g.from)+"/sendMail", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := g.hc.Do(req)
	if err != nil {
		return fmt.Errorf("graph: envoi: %s", scrub(err.Error(), tok))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("graph: envoi refusé (HTTP %d, %s)", resp.StatusCode, graphErr(raw))
	}
	return nil
}

// graphErr ne renvoie que le code d'erreur de Microsoft, jamais le jeton ni le secret.
func graphErr(raw []byte) string {
	var e struct {
		Error any    `json:"error"`
		Desc  string `json:"error_description"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return "réponse illisible"
	}
	switch v := e.Error.(type) {
	case map[string]any:
		return fmt.Sprintf("%v", v["code"])
	case string:
		d := e.Desc
		if len(d) > 120 {
			d = d[:120]
		}
		return v + " " + strings.SplitN(d, "\r", 2)[0]
	}
	return "?"
}

// scrub retire d'un message d'erreur toute valeur secrète.
func scrub(s string, secrets ...string) string {
	for _, v := range secrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

// ---- assemblage et file d'envoi ----

type notifier struct {
	senders []sender
	queue   chan message

	mu      sync.Mutex
	lastErr map[string]string
	lastOK  time.Time
}

// readEnvFile lit le fichier de secrets; vide si le chemin est vide ou le fichier absent.
func readEnvFile(path string) map[string]string {
	if path == "" {
		return map[string]string{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	return parseEnv(string(raw))
}

// newNotifier construit les canaux disponibles d'après le fichier d'environnement.
// Sans fichier ou sans clés, les notifications sont simplement désactivées.
func newNotifier(path string) (*notifier, []string) {
	n := &notifier{queue: make(chan message, 64), lastErr: map[string]string{}}
	var notes []string
	if path == "" {
		return n, []string{"notifications désactivées (-secrets non renseigné)"}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return n, []string{fmt.Sprintf("notifications désactivées (%v)", err)}
	}
	env := parseEnv(string(raw))
	hc := &http.Client{Timeout: 20 * time.Second}

	if topic := env["NTFY_TOPIC"]; topic != "" {
		srv := env["NTFY_SERVER"]
		if srv == "" {
			srv = "https://ntfy.sh"
		}
		n.senders = append(n.senders, &ntfySender{server: srv, topic: topic, token: env["NTFY_TOKEN"], hc: hc})
		notes = append(notes, "ntfy actif")
	} else {
		notes = append(notes, "ntfy inactif (NTFY_TOPIC absent)")
	}
	if env["GRAPH_TENANT_ID"] != "" && env["GRAPH_CLIENT_ID"] != "" && env["GRAPH_CLIENT_SECRET"] != "" &&
		env["MAIL_FROM"] != "" && env["MAIL_TO"] != "" {
		var to []string
		for _, a := range strings.Split(env["MAIL_TO"], ",") {
			if a = strings.TrimSpace(a); a != "" {
				to = append(to, a)
			}
		}
		n.senders = append(n.senders, &graphSender{tenant: env["GRAPH_TENANT_ID"], clientID: env["GRAPH_CLIENT_ID"],
			secret: env["GRAPH_CLIENT_SECRET"], from: env["MAIL_FROM"], to: to, hc: hc})
		notes = append(notes, "mail Graph actif")
	} else {
		notes = append(notes, "mail inactif (GRAPH_* / MAIL_FROM / MAIL_TO incomplets)")
	}
	return n, notes
}

func (n *notifier) enabled() bool { return len(n.senders) > 0 }

// sendAll envoie sur tous les canaux et renvoie les erreurs; chaque canal est indépendant.
func (n *notifier) sendAll(m message) error {
	_, err := n.sendTo(n.senders, m)
	return err
}

// sendTo envoie sur les canaux donnés et renvoie ceux qui ont échoué, pour ne réessayer qu'eux.
func (n *notifier) sendTo(senders []sender, m message) ([]sender, error) {
	var errs []string
	var failed []sender
	for _, s := range senders {
		if err := s.Send(m); err != nil {
			errs = append(errs, err.Error())
			failed = append(failed, s)
			n.mu.Lock()
			n.lastErr[s.Name()] = err.Error()
			n.mu.Unlock()
		} else {
			n.mu.Lock()
			delete(n.lastErr, s.Name())
			n.lastOK = time.Now()
			n.mu.Unlock()
		}
	}
	if len(errs) > 0 {
		return failed, errors.New(strings.Join(errs, " | "))
	}
	return nil, nil
}

// post met le message en file; ne bloque jamais (file pleine: message abandonné et journalisé).
func (n *notifier) post(m message) {
	if !n.enabled() {
		return
	}
	select {
	case n.queue <- m:
	default:
		logf("ATTENTION: file de notifications pleine, message abandonné: %s", m.Subject)
	}
}

// work envoie les messages en attente, avec trois essais espacés en cas d'échec réseau.
func (n *notifier) work() {
	for m := range n.queue {
		if err := n.deliver(m, func(try int) { time.Sleep(time.Duration(try+1) * 20 * time.Second) }); err != nil {
			logf("ATTENTION: notification non envoyée (%s): %v", m.Subject, err)
		}
	}
}

// deliver envoie un message avec trois essais. Un essai ne reprend QUE les canaux en échec: un canal
// qui a réussi ne reçoit jamais le message une deuxième fois (sinon un canal défaillant fait
// répéter le message sur les autres: 3 notifications et 3 mails pour une seule alerte).
func (n *notifier) deliver(m message, pause func(try int)) error {
	pending := n.senders
	var err error
	for try := 0; try < 3; try++ {
		pending, err = n.sendTo(pending, m)
		if err == nil {
			return nil
		}
		if try < 2 {
			pause(try)
		}
	}
	return err
}

// sendNow envoie tout de suite, avec un délai maximal: sert juste avant une extinction.
func (n *notifier) sendNow(m message, limit time.Duration) {
	if !n.enabled() {
		return
	}
	done := make(chan struct{})
	go func() { n.sendAll(m); close(done) }()
	select {
	case <-done:
	case <-time.After(limit):
		logf("ATTENTION: notification d'arrêt non confirmée après %s", limit)
	}
}

func (n *notifier) status() (okAt time.Time, errs map[string]string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	errs = map[string]string{}
	for k, v := range n.lastErr {
		errs[k] = v
	}
	return n.lastOK, errs
}

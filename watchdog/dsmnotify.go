package main

import (
	"fmt"
	"strings"
	"time"
)

// Notification dans le centre de notifications de DSM (cloche en haut à droite), en plus de ntfy et du
// mail (décision du 28/10/2026: « signifier la panne au NAS »). Elle passe par l'outil de DSM
// synodsmnotify, lancé SANS interpréteur de commandes (arguments séparés: aucun risque d'injection).
// Seuls les messages importants (priorité 4 et 5: alertes critiques, arrêt automatique, redémarrage
// après arrêt) y sont envoyés, pour ne pas encombrer le centre de notifications.
//
// Syntaxe relevée sur le NAS: synodsmnotify [-c className] [-t tag] [-e true/false] [-b true/false]
// [-p html/plain] [--fn fn] [-l info/warn/error] user/group title msg ; un groupe s'écrit @NOM.

type dsmSender struct {
	bin   string
	group string
	run   func(timeout time.Duration, name string, args ...string) (string, error)
}

func newDSMSender() *dsmSender {
	return &dsmSender{bin: "/usr/syno/bin/synodsmnotify", group: "@administrators", run: run}
}

func (d *dsmSender) Name() string { return "DSM" }

func (d *dsmSender) Send(m message) error {
	if m.Priority < 4 {
		return nil
	}
	level := "warn"
	if m.Priority >= 5 {
		level = "error"
	}
	title := strings.TrimSpace(m.Subject)
	if title == "" {
		title = "Surveillance du meuble"
	}
	body := strings.TrimSpace(m.Short)
	if body == "" {
		body = strings.TrimSpace(m.Body)
	}
	if out, err := d.run(10*time.Second, d.bin, "-l", level, d.group, title, body); err != nil {
		return fmt.Errorf("synodsmnotify: %v %s", err, strings.TrimSpace(out))
	}
	return nil
}

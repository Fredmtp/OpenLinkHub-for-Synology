package main

import (
	"errors"
	"testing"
)

// fakeSender compte ses envois; il échoue failFirst fois avant de réussir (-1 = toujours).
type fakeSender struct {
	name      string
	failFirst int
	calls     int
}

func (f *fakeSender) Name() string { return f.name }
func (f *fakeSender) Send(message) error {
	f.calls++
	if f.failFirst < 0 || f.calls <= f.failFirst {
		return errors.New("échec simulé")
	}
	return nil
}

func newTestNotifier(senders ...sender) *notifier {
	return &notifier{senders: senders, queue: make(chan message, 4), lastErr: map[string]string{}}
}

// Régression du 29/10/2026: un canal en échec faisait renvoyer le message sur les canaux qui avaient réussi.
func TestDeliverNeRenvoiePasSurLesCanauxReussis(t *testing.T) {
	ok := &fakeSender{name: "ntfy"}
	ko := &fakeSender{name: "mail", failFirst: -1}
	n := newTestNotifier(ok, ko)
	if err := n.deliver(message{Subject: "x"}, func(int) {}); err == nil {
		t.Fatal("le canal en échec permanent doit renvoyer une erreur")
	}
	if ok.calls != 1 {
		t.Errorf("le canal qui a réussi a reçu %d envois, attendu 1", ok.calls)
	}
	if ko.calls != 3 {
		t.Errorf("le canal en échec a eu %d essais, attendu 3", ko.calls)
	}
}

func TestDeliverCanalQuiSeRetablit(t *testing.T) {
	ok := &fakeSender{name: "ntfy"}
	flaky := &fakeSender{name: "mail", failFirst: 1}
	n := newTestNotifier(ok, flaky)
	if err := n.deliver(message{Subject: "x"}, func(int) {}); err != nil {
		t.Fatalf("le 2e essai doit réussir: %v", err)
	}
	if ok.calls != 1 || flaky.calls != 2 {
		t.Errorf("envois: ntfy=%d (attendu 1), mail=%d (attendu 2)", ok.calls, flaky.calls)
	}
}

func TestDeliverToutReussitDuPremierCoup(t *testing.T) {
	a, b := &fakeSender{name: "ntfy"}, &fakeSender{name: "mail"}
	n := newTestNotifier(a, b)
	pauses := 0
	if err := n.deliver(message{Subject: "x"}, func(int) { pauses++ }); err != nil {
		t.Fatal(err)
	}
	if a.calls != 1 || b.calls != 1 || pauses != 0 {
		t.Errorf("envois %d et %d, pauses %d: attendu 1, 1 et 0", a.calls, b.calls, pauses)
	}
}

package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type dsmCall struct {
	name string
	args []string
}

func fakeDSM(calls *[]dsmCall, err error) *dsmSender {
	return &dsmSender{bin: "/usr/syno/bin/synodsmnotify", group: "@administrators",
		run: func(_ time.Duration, name string, args ...string) (string, error) {
			*calls = append(*calls, dsmCall{name, args})
			return "sortie", err
		}}
}

func TestDSMSenderOnlyForImportantMessages(t *testing.T) {
	var calls []dsmCall
	s := fakeDSM(&calls, nil)
	for _, p := range []int{1, 2, 3} {
		if err := s.Send(message{Subject: "x", Short: "y", Priority: p}); err != nil {
			t.Fatal(err)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("priorités 1 à 3: rien dans DSM, %d appel(s)", len(calls))
	}
	s.Send(message{Subject: "Alerte", Short: "texte court", Priority: 4})
	s.Send(message{Subject: "Arrêt", Short: "ARRÊT du NAS", Priority: 5})
	want := []dsmCall{
		{"/usr/syno/bin/synodsmnotify", []string{"-l", "warn", "@administrators", "Alerte", "texte court"}},
		{"/usr/syno/bin/synodsmnotify", []string{"-l", "error", "@administrators", "Arrêt", "ARRÊT du NAS"}},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("appels: %+v, attendu %+v", calls, want)
	}
}

func TestDSMSenderArgumentsStaySeparate(t *testing.T) {
	// Un texte avec guillemets, espaces ou ; $() reste UN argument: aucun interpréteur de commandes.
	var calls []dsmCall
	s := fakeDSM(&calls, nil)
	evil := `a"; rm -rf / ; $(reboot) 'b'`
	s.Send(message{Subject: evil, Short: evil, Priority: 5})
	if len(calls) != 1 || len(calls[0].args) != 5 || calls[0].args[3] != evil || calls[0].args[4] != evil {
		t.Fatalf("arguments: %+v", calls)
	}
}

func TestDSMSenderFallbacks(t *testing.T) {
	var calls []dsmCall
	s := fakeDSM(&calls, nil)
	s.Send(message{Body: "texte long", Priority: 4})
	if calls[0].args[3] != "Surveillance du meuble" || calls[0].args[4] != "texte long" {
		t.Fatalf("titre ou texte par défaut: %+v", calls[0].args)
	}
}

func TestDSMSenderReportsFailure(t *testing.T) {
	var calls []dsmCall
	s := fakeDSM(&calls, errors.New("exit status 1"))
	err := s.Send(message{Subject: "x", Short: "y", Priority: 5})
	if err == nil || !strings.Contains(err.Error(), "synodsmnotify") || !strings.Contains(err.Error(), "sortie") {
		t.Fatalf("erreur attendue avec la sortie de l'outil: %v", err)
	}
	if s.Name() != "DSM" {
		t.Fatal("nom du canal")
	}
}

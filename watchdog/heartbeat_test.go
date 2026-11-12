package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHeartbeatWritesDateAndRespectsInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "olh-watchdog.beat")
	var b beater
	t0 := time.Unix(1_800_000_000, 0)
	if err := b.tick(path, t0); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(raw)) != "1800000000" {
		t.Fatalf("contenu: %q %v", raw, err)
	}
	// Moins de 20 s plus tard: pas de nouvelle écriture.
	os.Remove(path)
	b.tick(path, t0.Add(10*time.Second))
	if _, err := os.Stat(path); err == nil {
		t.Fatal("pas d'écriture avant 20 s")
	}
	b.tick(path, t0.Add(20*time.Second))
	if _, err := os.Stat(path); err != nil {
		t.Fatal("écriture attendue après 20 s")
	}
}

func TestHeartbeatDisabledWithoutPath(t *testing.T) {
	var b beater
	if err := b.tick("", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	markStopping("") // ne doit rien faire ni paniquer
}

func TestHeartbeatReportsAnUnwritableFile(t *testing.T) {
	var b beater
	if err := b.tick(filepath.Join(t.TempDir(), "absent", "beat"), time.Unix(1, 0)); err == nil {
		t.Fatal("une erreur d'écriture doit être renvoyée")
	}
}

func TestMarkStoppingCreatesTheStopFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "olh-watchdog.beat")
	markStopping(path)
	if _, err := os.Stat(path + ".stop"); err != nil {
		t.Fatalf("fichier .stop attendu: %v", err)
	}
}

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCombatJournalPreservesExistingSessionEvidence(t *testing.T) {
	directory := t.TempDir()
	journal, err := openCombatJournal(directory, "aw-test")
	if err != nil {
		t.Fatal(err)
	}
	const previous = "{\"sequence\":\"1\"}\n"
	if _, err := journal.WriteString(previous); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if replacement, err := openCombatJournal(directory, "aw-test"); !errors.Is(err, os.ErrExist) || replacement != nil {
		t.Fatal("reused sequence space overwrote old evidence", replacement, err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "aw-test.jsonl"))
	if err != nil || string(data) != previous {
		t.Fatal(string(data), err)
	}
	stat, err := os.Stat(filepath.Join(directory, "aw-test.jsonl"))
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal(stat, err)
	}
	next, err := openCombatJournal(directory, "aw-next")
	if err != nil {
		t.Fatal("fresh session journal unavailable", err)
	}
	next.Close()
}

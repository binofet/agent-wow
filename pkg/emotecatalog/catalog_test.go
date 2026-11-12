package emotecatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogPinnedTextIDsAndDetachedOutput(t *testing.T) {
	c := All()
	if len(c.Emotes) != 252 || c.SchemaVersion != 1 || c.Kind != "TextEmotes" || c.Provenance.SourceSHA256 != "7b17defee7cfb58fe42bcfbc2aab304b210eb922446cdf4193925d220b9bdf47" || c.Provenance.SourcePath != "src/server/shared/SharedDefines.h" || c.Provenance.SourceCommit != nil {
		t.Fatal("catalog lost its pinned complete primary-source facts", c.Provenance, len(c.Emotes))
	}
	for id, name := range map[uint32]string{1: "AGREE", 2: "AMAZE", 17: "BOW", 21: "CHEER", 34: "DANCE", 60: "LAUGH", 101: "WAVE", 453: "YW"} {
		if entry, ok := Lookup(id); !ok || entry.Name != name || entry.ID != id {
			t.Fatalf("wrong text ID %d: %+v", id, entry)
		}
	}
	for _, id := range []uint32{0, 144, 65535, ^uint32(0)} {
		if _, ok := Lookup(id); ok {
			t.Fatalf("invented text ID %d", id)
		}
	}
	c.Emotes[0].Name = "forged"
	if entry, _ := Lookup(1); entry.Name != "AGREE" || All().Emotes[0].Name != "AGREE" {
		t.Fatal("caller mutated embedded facts")
	}
}

func runGenerator(t *testing.T, source []byte, commit string) ([]byte, error) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable for offline generator check")
	}
	dir := t.TempDir()
	input, output := filepath.Join(dir, "SharedDefines.h"), filepath.Join(dir, "emotes.json")
	if err := os.WriteFile(input, source, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-B", filepath.Join("..", "..", "dev", "build-emote-catalog"), "--source", input, "--output", output}
	if commit != "" {
		args = append(args, "--source-commit", commit)
	}
	err = exec.Command(python, args...).Run()
	data, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err != nil && string(data) != "unchanged" {
		t.Fatal("invalid source replaced the previous catalog")
	}
	return data, err
}

func TestGeneratorPinsCompleteInputAndOnlyTextEnum(t *testing.T) {
	header := []byte("// enum TextEmotes { ignored }\nenum TextEmotes { TEXT_EMOTE_WAVE=101, /* note */ TEXT_EMOTE_DANCE=0x22, TEXT_EMOTE_LAUGH=60, };\nenum Emote { EMOTE_STATE_DANCE=10 };\n")
	commit := strings.Repeat("a", 40)
	data, err := runGenerator(t, header, commit)
	if err != nil {
		t.Fatal(err)
	}
	var got Catalog
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Emotes) != 3 || got.Emotes[0] != (Entry{34, "DANCE"}) || got.Emotes[1] != (Entry{60, "LAUGH"}) || got.Emotes[2] != (Entry{101, "WAVE"}) || got.Provenance.SourceSHA256 != fmt.Sprintf("%x", sha256.Sum256(header)) || got.Provenance.SourceCommit == nil || *got.Provenance.SourceCommit != commit || !strings.Contains(got.Provenance.UpstreamURL, "/"+commit+"/") {
		t.Fatal("wrong enum, order, or provenance", string(data))
	}
	again, err := runGenerator(t, header, commit)
	if err != nil || !bytes.Equal(data, again) {
		t.Fatal("generator output depends on time/private local input path", err)
	}
}

func TestGeneratorRejectsPartialOrAmbiguousSource(t *testing.T) {
	for _, body := range []string{"", "TEXT_EMOTE_ZERO=0", "TEXT_EMOTE_A=1,TEXT_EMOTE_B=1", "TEXT_EMOTE_A=1,TEXT_EMOTE_A=2", "TEXT_EMOTE_A=1,BOGUS=2", "TEXT_EMOTE_A=1+2", "TEXT_EMOTE_A=-1", "TEXT_EMOTE_A=4294967296"} {
		t.Run(body, func(t *testing.T) {
			if _, err := runGenerator(t, []byte("enum TextEmotes { "+body+" };"), ""); err == nil {
				t.Fatal("accepted incomplete or ambiguous facts")
			}
		})
	}
	for _, header := range []string{"enum Emote { TEXT_EMOTE_A=1 };", "enum TextEmotes { TEXT_EMOTE_A=1", "enum TextEmotes { TEXT_EMOTE_A=1 }; enum TextEmotes { TEXT_EMOTE_B=2 };"} {
		if _, err := runGenerator(t, []byte(header), ""); err == nil {
			t.Fatal("accepted missing/duplicate/incomplete TextEmotes enum")
		}
	}
	if _, err := runGenerator(t, []byte("enum TextEmotes { TEXT_EMOTE_A=1 };"), "unverified-master"); err == nil {
		t.Fatal("accepted non-commit provenance")
	}
}

func TestCatalogMatchesProvidedPrimaryHeader(t *testing.T) {
	path := os.Getenv("BUDDY_TEXT_EMOTES_SOURCE")
	if path == "" {
		t.Skip("optional offline primary header not supplied")
	}
	header, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := runGenerator(t, header, "")
	if err != nil || !bytes.Equal(data, source) {
		t.Fatal("embedded catalog is not the complete deterministic primary-source extraction", err)
	}
}

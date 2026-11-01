// Package emotecatalog exposes the public Wrath TextEmotes IDs shared by script
// metadata and the ordinary CMSG_TEXT_EMOTE bridge, not animation IDs.
package emotecatalog

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
)

//go:embed emotes.json
var source []byte

type Entry struct {
	ID   uint32 `json:"id"`
	Name string `json:"name"`
}

type Provenance struct {
	UpstreamURL  string  `json:"upstreamUrl"`
	SourcePath   string  `json:"sourcePath"`
	SourceSHA256 string  `json:"sourceSha256"`
	SourceCommit *string `json:"sourceCommit"` // nil explicitly means unknown.
}

type Catalog struct {
	SchemaVersion int        `json:"schemaVersion"`
	Kind          string     `json:"kind"`
	Provenance    Provenance `json:"provenance"`
	Emotes        []Entry    `json:"emotes"`
}

var catalog, byID = load()
var revision = fmt.Sprintf("%x", sha256.Sum256(source))

// Revision pins the exact shared JSON, including its source provenance.
func Revision() string { return revision }

func load() (Catalog, map[uint32]Entry) {
	var catalog Catalog
	if err := json.Unmarshal(source, &catalog); err != nil {
		panic(fmt.Errorf("invalid embedded TextEmotes catalog: %w", err))
	}
	if catalog.SchemaVersion != 1 || catalog.Kind != "TextEmotes" || len(catalog.Emotes) == 0 {
		panic("invalid embedded TextEmotes catalog identity")
	}
	ids, names := make(map[uint32]Entry, len(catalog.Emotes)), make(map[string]bool, len(catalog.Emotes))
	validName := regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	var prior uint32
	for _, entry := range catalog.Emotes {
		if entry.ID <= prior || names[entry.Name] || !validName.MatchString(entry.Name) {
			panic("invalid embedded TextEmotes ID/name")
		}
		ids[entry.ID], names[entry.Name], prior = entry, true, entry.ID
	}
	return catalog, ids
}

// Lookup validates an ordinary text emote ID. A recognized ID is not proof of
// server acceptance or visible animation; the server still handles the packet.
func Lookup(id uint32) (Entry, bool) {
	entry, ok := byID[id]
	return entry, ok
}

// All returns a detached catalog. Unknown source commits remain explicit; the
// provenance hash pins the complete header actually consumed by the generator.
func All() Catalog {
	result := catalog
	result.Emotes = append([]Entry{}, catalog.Emotes...)
	if catalog.Provenance.SourceCommit != nil {
		commit := *catalog.Provenance.SourceCommit
		result.Provenance.SourceCommit = &commit
	}
	return result
}

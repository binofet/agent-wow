// Package taxireference exposes immutable public client reference facts.
// A source's claimed build, map membership, French label and undiscounted base
// price prove no deployed realm version, live taxi knowledge, offered route,
// fare, walkable geometry or successful flight.
package taxireference

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

type Node struct {
	ID                uint32  `json:"id"`
	MapID             uint32  `json:"mapId"`
	X                 float64 `json:"x"`
	Y                 float64 `json:"y"`
	Z                 float64 `json:"z"`
	Name              string  `json:"name"`
	MapReferenceKnown bool    `json:"mapReferenceKnown"`
}

type Path struct {
	ID             uint32 `json:"id"`
	Source         uint32 `json:"source"`
	Destination    uint32 `json:"destination"`
	BaseCopper     uint32 `json:"baseCopper"`
	EndpointsKnown bool   `json:"endpointsKnown"`
}

type Provenance struct {
	Repository  string `json:"repository"`
	Commit      string `json:"commit"`
	NodesSHA256 string `json:"nodesSHA256"`
	PathsSHA256 string `json:"pathsSHA256"`
	MapSHA256   string `json:"mapSHA256"`
	NodeURL     string `json:"nodeURL"`
	PathURL     string `json:"pathURL"`
	MapSource   string `json:"mapSource"`
}

type Quality struct {
	Nodes                  uint32 `json:"nodes"`
	Paths                  uint32 `json:"paths"`
	UnnamedNodes           uint32 `json:"unnamedNodes"`
	UnmappedNodes          uint32 `json:"unmappedNodes"`
	InvalidEndpointPaths   uint32 `json:"invalidEndpointPaths"`
	DuplicateEndpointPairs uint32 `json:"duplicateEndpointPairs"`
	ZeroPricePaths         uint32 `json:"zeroPricePaths"`
}

type Info struct {
	Format       uint32     `json:"format"`
	ClaimedBuild uint32     `json:"claimedBuild"`
	NameLocale   string     `json:"nameLocale"`
	Provenance   Provenance `json:"provenance"`
	Quality      Quality    `json:"quality"`
}

type Catalog struct {
	info  Info
	nodes map[uint32]Node
	paths map[uint32]Path
	from  map[uint32][]Path
}

func (c *Catalog) Node(id uint32) (Node, bool) {
	if c == nil {
		return Node{}, false
	}
	node, ok := c.nodes[id]
	return node, ok
}

func (c *Catalog) Path(id uint32) (Path, bool) {
	if c == nil {
		return Path{}, false
	}
	path, ok := c.paths[id]
	return path, ok
}

// PathsFrom returns every matching raw reference row, ordered by its path ID.
// This factual key order is not a route preference. Duplicate endpoint pairs,
// zero base prices and rows with unknown endpoints remain present.
func (c *Catalog) PathsFrom(source uint32) []Path {
	if c == nil {
		return nil
	}
	return append([]Path(nil), c.from[source]...)
}

// Info contains values only; callers cannot mutate the catalog's metadata.
func (c *Catalog) Info() Info {
	if c == nil {
		return Info{}
	}
	return c.info
}

// Presence is separate from zero: zero copper and sentinel endpoint IDs are
// actual retained facts, never defaults for missing JSON fields.
type nodeRecord struct {
	ID                *uint32  `json:"id"`
	MapID             *uint32  `json:"mapId"`
	X                 *float64 `json:"x"`
	Y                 *float64 `json:"y"`
	Z                 *float64 `json:"z"`
	Name              *string  `json:"name"`
	MapReferenceKnown *bool    `json:"mapReferenceKnown"`
}

type pathRecord struct {
	ID             *uint32 `json:"id"`
	Source         *uint32 `json:"source"`
	Destination    *uint32 `json:"destination"`
	BaseCopper     *uint32 `json:"baseCopper"`
	EndpointsKnown *bool   `json:"endpointsKnown"`
}

type qualityRecord struct {
	Nodes                  *uint32 `json:"nodes"`
	Paths                  *uint32 `json:"paths"`
	UnnamedNodes           *uint32 `json:"unnamedNodes"`
	UnmappedNodes          *uint32 `json:"unmappedNodes"`
	InvalidEndpointPaths   *uint32 `json:"invalidEndpointPaths"`
	DuplicateEndpointPairs *uint32 `json:"duplicateEndpointPairs"`
	ZeroPricePaths         *uint32 `json:"zeroPricePaths"`
}

func decodeCatalog(data []byte) (*Catalog, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("taxi reference JSON must be UTF-8")
	}
	var document struct {
		Format       uint32         `json:"format"`
		ClaimedBuild uint32         `json:"claimedBuild"`
		NameLocale   string         `json:"nameLocale"`
		Provenance   Provenance     `json:"provenance"`
		Nodes        []nodeRecord   `json:"nodes"`
		Paths        []pathRecord   `json:"paths"`
		Quality      *qualityRecord `json:"quality"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode taxi reference: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("taxi reference requires one JSON document")
	}
	if document.Format != 1 || document.ClaimedBuild != 12340 || document.NameLocale != "frFR" {
		return nil, fmt.Errorf("taxi reference requires format 1, claimed build 12340 and explicit frFR names")
	}
	if err := validateProvenance(document.Provenance); err != nil {
		return nil, err
	}
	if len(document.Nodes) == 0 || len(document.Paths) == 0 {
		return nil, fmt.Errorf("taxi reference node and path rows are required")
	}
	q := document.Quality
	if q == nil || q.Nodes == nil || q.Paths == nil || q.UnnamedNodes == nil || q.UnmappedNodes == nil ||
		q.InvalidEndpointPaths == nil || q.DuplicateEndpointPairs == nil || q.ZeroPricePaths == nil {
		return nil, fmt.Errorf("all taxi reference quality counters are required")
	}
	declared := Quality{*q.Nodes, *q.Paths, *q.UnnamedNodes, *q.UnmappedNodes, *q.InvalidEndpointPaths, *q.DuplicateEndpointPairs, *q.ZeroPricePaths}
	c := &Catalog{nodes: make(map[uint32]Node), paths: make(map[uint32]Path), from: make(map[uint32][]Path)}
	actual := Quality{}
	for i, row := range document.Nodes {
		if row.ID == nil || row.MapID == nil || row.X == nil || row.Y == nil || row.Z == nil || row.Name == nil || row.MapReferenceKnown == nil {
			return nil, fmt.Errorf("taxi reference node row %d has missing facts", i)
		}
		if *row.ID == 0 || !finiteCoordinate(*row.X) || !finiteCoordinate(*row.Y) || !finiteCoordinate(*row.Z) {
			return nil, fmt.Errorf("taxi reference node row %d has invalid identity or coordinates", i)
		}
		if _, exists := c.nodes[*row.ID]; exists {
			return nil, fmt.Errorf("duplicate taxi reference node ID %d", *row.ID)
		}
		node := Node{*row.ID, *row.MapID, *row.X, *row.Y, *row.Z, *row.Name, *row.MapReferenceKnown}
		c.nodes[node.ID] = node
		actual.Nodes++
		if node.Name == "" {
			actual.UnnamedNodes++
		}
		if !node.MapReferenceKnown {
			actual.UnmappedNodes++
		}
	}
	type endpoints struct{ source, destination uint32 }
	pairs := make(map[endpoints]uint32)
	for i, row := range document.Paths {
		if row.ID == nil || row.Source == nil || row.Destination == nil || row.BaseCopper == nil || row.EndpointsKnown == nil || *row.ID == 0 {
			return nil, fmt.Errorf("taxi reference path row %d has missing facts or invalid identity", i)
		}
		if _, exists := c.paths[*row.ID]; exists {
			return nil, fmt.Errorf("duplicate taxi reference path ID %d", *row.ID)
		}
		path := Path{*row.ID, *row.Source, *row.Destination, *row.BaseCopper, *row.EndpointsKnown}
		_, sourceKnown := c.nodes[path.Source]
		_, destinationKnown := c.nodes[path.Destination]
		if path.EndpointsKnown != (sourceKnown && destinationKnown) {
			return nil, fmt.Errorf("taxi reference path %d endpoint flag disagrees with retained nodes", path.ID)
		}
		c.paths[path.ID] = path
		c.from[path.Source] = append(c.from[path.Source], path)
		pairs[endpoints{path.Source, path.Destination}]++
		actual.Paths++
		if !path.EndpointsKnown {
			actual.InvalidEndpointPaths++
		}
		if path.BaseCopper == 0 {
			actual.ZeroPricePaths++
		}
	}
	for _, count := range pairs {
		if count >= 2 {
			actual.DuplicateEndpointPairs++
		}
	}
	if actual != declared {
		return nil, fmt.Errorf("taxi reference quality counters disagree: declared %+v, actual %+v", declared, actual)
	}
	for source := range c.from {
		sort.Slice(c.from[source], func(i, j int) bool { return c.from[source][i].ID < c.from[source][j].ID })
	}
	c.info = Info{document.Format, document.ClaimedBuild, document.NameLocale, document.Provenance, actual}
	return c, nil
}

func finiteCoordinate(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func validateProvenance(p Provenance) error {
	if p.Repository == "" || strings.TrimSpace(p.Repository) != p.Repository || !canonicalHex(p.Commit, 40) ||
		!canonicalHex(p.NodesSHA256, 64) || !canonicalHex(p.PathsSHA256, 64) || !canonicalHex(p.MapSHA256, 64) {
		return fmt.Errorf("taxi reference requires repository, immutable commit and canonical source SHA-256 pins")
	}
	for _, source := range []string{p.NodeURL, p.PathURL, p.MapSource} {
		parsed, err := url.Parse(source)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return fmt.Errorf("taxi reference requires explicit HTTPS source URLs")
		}
	}
	return nil
}

func canonicalHex(value string, length int) bool {
	if len(value) != length || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

//go:embed reference.json
var referenceJSON []byte

var defaultOnce sync.Once
var defaultCatalog *Catalog
var defaultError error

func Default() (*Catalog, error) {
	defaultOnce.Do(func() { defaultCatalog, defaultError = decodeCatalog(referenceJSON) })
	return defaultCatalog, defaultError
}

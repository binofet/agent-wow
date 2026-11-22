package taxireference

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEmbeddedReferenceRetainsPinnedMetadataAndActualOutliers(t *testing.T) {
	c, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	info := c.Info()
	quality := Quality{Nodes: 364, Paths: 915, UnnamedNodes: 6, UnmappedNodes: 6,
		InvalidEndpointPaths: 10, DuplicateEndpointPairs: 4, ZeroPricePaths: 145}
	if info.Format != 1 || info.ClaimedBuild != 12340 || info.NameLocale != "frFR" || info.Quality != quality ||
		len(c.nodes) != 364 || len(c.paths) != 915 {
		t.Fatal("embedded facts or explicit source claims changed", info)
	}
	const commit = "c98c8e4972086195d1306b7830d3e8452026b68d"
	if info.Provenance.Repository != "https://github.com/DreamCoreRev/EonsDBC" || info.Provenance.Commit != commit ||
		info.Provenance.NodesSHA256 != "63a096b629298cdd254687658947c3b3ac8b40b483de012a8a5194aa0467cb1a" ||
		info.Provenance.PathsSHA256 != "1af5ba71fca5684a432b9d3f449aaeb21e4c091532e4b5795335be200d88dd5d" ||
		info.Provenance.MapSHA256 != "261bd7c2c9dbdba1f2966ca21106673ed722c2e3681d227f1eed261b6ce870d5" ||
		info.Provenance.NodeURL != "https://raw.githubusercontent.com/DreamCoreRev/EonsDBC/"+commit+"/DBFilesClient/dbc/TaxiNodes.dbc" ||
		info.Provenance.PathURL != "https://raw.githubusercontent.com/DreamCoreRev/EonsDBC/"+commit+"/DBFilesClient/dbc/TaxiPath.dbc" ||
		info.Provenance.MapSource != "https://github.com/wowgaming/client-data/releases/download/v20.0/Data.zip" {
		t.Fatal("embedded source provenance is no longer the reviewed immutable reference", info.Provenance)
	}
	first, known := c.Node(1)
	if !known || first.Name != "Abbaye de Comté-du-nord" || first.MapID != 0 || !first.MapReferenceKnown ||
		first.X != -8888.98046875 || first.Y != -0.5400000214576721 || first.Z != 94.38999938964844 {
		t.Fatal("lookup changed the actual French label or binary32-derived coordinates", first, known)
	}
	for _, id := range []uint32{216, 217, 220, 238, 353, 357} {
		if node, ok := c.Node(id); !ok || node.Name != "" {
			t.Fatal("blank reference label was discarded or replaced by a guessed name", id, node, ok)
		}
	}
	for _, id := range []uint32{81, 90, 161, 165, 178, 204} {
		if node, ok := c.Node(id); !ok || node.MapReferenceKnown {
			t.Fatal("a raw unmapped node was removed or represented as map-validated", id, node, ok)
		}
	}
	if node, _ := c.Node(161); node.MapID != 654135852 || node.Name != "Quête - Allié dragon du Néant - début" {
		t.Fatal("unusual raw map identity or locale was normalized away", node)
	}
	for _, id := range []uint32{1247, 1248, 1252, 1813, 1814, 1815, 1816, 1817, 1818, 1819} {
		path, ok := c.Path(id)
		if !ok || path.EndpointsKnown || path.Destination != ^uint32(0) || path.BaseCopper != 0 {
			t.Fatal("raw invalid-endpoint/zero-price row was filtered or repaired", id, path, ok)
		}
	}
	if rows := c.PathsFrom(0); len(rows) != 1 || rows[0].ID != 1813 || rows[0].Source != 0 {
		t.Fatal("source-zero sentinel row cannot be inspected", rows)
	}
	var duplicate []Path
	for _, path := range c.PathsFrom(175) {
		if path.Destination == 178 {
			duplicate = append(duplicate, path)
		}
	}
	want := []Path{{ID: 733, Source: 175, Destination: 178, BaseCopper: 0, EndpointsKnown: true},
		{ID: 793, Source: 175, Destination: 178, BaseCopper: 0, EndpointsKnown: true}}
	if !reflect.DeepEqual(duplicate, want) {
		t.Fatal("duplicate endpoint paths were collapsed, filtered or repriced", duplicate)
	}
	// Endpoint membership is independent of whether the endpoint's raw map
	// occurs in the pinned Map table; neither fact is a geometry qualification.
	if node, _ := c.Node(178); node.MapReferenceKnown {
		t.Fatal("fixture lost the endpoint/map-reference distinction", node)
	}
	zeroPrices := 0
	for _, path := range c.paths {
		if path.BaseCopper == 0 {
			zeroPrices++
		}
	}
	if zeroPrices != 145 {
		t.Fatal("zero undiscounted base prices were dropped or defaulted", zeroPrices)
	}
}

func TestCatalogLookupsReturnDetachedValuesWithoutFallbacks(t *testing.T) {
	c, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	node, _ := c.Node(1)
	node.Name, node.X = "caller edit", 0
	if unchanged, _ := c.Node(1); unchanged.Name == node.Name || unchanged.X == node.X {
		t.Fatal("caller changed an indexed node", unchanged)
	}
	path, _ := c.Path(733)
	path.BaseCopper = 999
	if unchanged, _ := c.Path(733); unchanged.BaseCopper != 0 {
		t.Fatal("caller changed an indexed zero base price", unchanged)
	}
	rows := c.PathsFrom(175)
	if len(rows) == 0 {
		t.Fatal("fixture source lost all reference paths")
	}
	original := rows[0]
	rows[0].ID, rows[0].BaseCopper = 1, 999
	if unchanged := c.PathsFrom(175); unchanged[0] != original {
		t.Fatal("outgoing slice aliases the immutable index", unchanged)
	}
	info := c.Info()
	info.Provenance.Repository, info.Provenance.Commit, info.Quality.Nodes = "caller edit", "changed", 0
	if unchanged := c.Info(); unchanged.Provenance.Repository == info.Provenance.Repository || unchanged.Quality.Nodes != 364 {
		t.Fatal("metadata aliases the immutable source", unchanged)
	}
	for _, id := range []uint32{0, ^uint32(0)} {
		if node, ok := c.Node(id); ok || node != (Node{}) {
			t.Fatal("unknown node lookup invented a fallback", id, node, ok)
		}
		if path, ok := c.Path(id); ok || path != (Path{}) {
			t.Fatal("unknown path lookup invented a fallback", id, path, ok)
		}
	}
	if rows := c.PathsFrom(^uint32(0)); len(rows) != 0 {
		t.Fatal("unknown source lookup invented outgoing paths", rows)
	}
	var missing *Catalog
	if _, ok := missing.Node(1); ok {
		t.Fatal("nil catalog claimed a known node")
	}
	if _, ok := missing.Path(1); ok || len(missing.PathsFrom(1)) != 0 || missing.Info() != (Info{}) {
		t.Fatal("nil catalog invented reference facts")
	}
}

func smallReferenceJSON(t *testing.T) []byte {
	t.Helper()
	commit := strings.Repeat("a", 40)
	document := struct {
		Info
		Nodes []Node `json:"nodes"`
		Paths []Path `json:"paths"`
	}{Info: Info{Format: 1, ClaimedBuild: 12340, NameLocale: "frFR",
		Provenance: Provenance{Repository: "https://example.com/reference", Commit: commit,
			NodesSHA256: strings.Repeat("b", 64), PathsSHA256: strings.Repeat("c", 64), MapSHA256: strings.Repeat("d", 64),
			NodeURL: "https://example.com/" + commit + "/nodes", PathURL: "https://example.com/" + commit + "/paths", MapSource: "https://example.com/maps"},
		Quality: Quality{Nodes: 2, Paths: 3, UnnamedNodes: 1, UnmappedNodes: 1, InvalidEndpointPaths: 1, DuplicateEndpointPairs: 1, ZeroPricePaths: 2}},
		Nodes: []Node{{ID: 1, Name: "Étage", MapReferenceKnown: true}, {ID: 2, MapID: 999}},
		Paths: []Path{{ID: 7, Source: 1, Destination: 2, BaseCopper: 10, EndpointsKnown: true},
			{ID: 5, Source: 1, Destination: 2, EndpointsKnown: true}, {ID: 8, Source: 0, Destination: ^uint32(0)}},
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCatalogIndexesAllRawPathsWithoutEndpointFilteringOrRanking(t *testing.T) {
	c, err := decodeCatalog(smallReferenceJSON(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.PathsFrom(1); !reflect.DeepEqual(got, []Path{
		{ID: 5, Source: 1, Destination: 2, EndpointsKnown: true},
		{ID: 7, Source: 1, Destination: 2, BaseCopper: 10, EndpointsKnown: true}}) {
		t.Fatal("index removed duplicate rows, changed raw prices or ignored factual ID order", got)
	}
	if got := c.PathsFrom(0); len(got) != 1 || got[0].ID != 8 || got[0].EndpointsKnown {
		t.Fatal("raw sentinel endpoints were hidden", got)
	}
}

func TestCatalogRejectsIncoherentOrMissingMetadataAndRowFacts(t *testing.T) {
	for _, name := range []string{"format", "build", "locale", "repository", "commit", "source hash", "source URL", "missing quality", "missing quality counter",
		"quality count", "duplicate groups", "duplicate node", "duplicate path", "zero node ID", "zero path ID", "missing coordinate", "null coordinate",
		"missing map", "missing name", "missing map flag", "missing price", "missing source", "missing endpoint flag", "false endpoint flag", "invented endpoint flag", "unknown field", "negative price"} {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(smallReferenceJSON(t), &doc); err != nil {
				t.Fatal(err)
			}
			nodes, paths := doc["nodes"].([]any), doc["paths"].([]any)
			node, path := nodes[0].(map[string]any), paths[0].(map[string]any)
			quality, provenance := doc["quality"].(map[string]any), doc["provenance"].(map[string]any)
			switch name {
			case "format":
				doc["format"] = 2
			case "build":
				doc["claimedBuild"] = 999
			case "locale":
				doc["nameLocale"] = "enUS"
			case "repository":
				delete(provenance, "repository")
			case "commit":
				provenance["commit"] = "master"
			case "source hash":
				provenance["nodesSHA256"] = strings.Repeat("g", 64)
			case "source URL":
				provenance["nodeURL"] = "http://example.com/unpinned"
			case "missing quality":
				delete(doc, "quality")
			case "missing quality counter":
				delete(quality, "zeroPricePaths")
			case "quality count":
				quality["nodes"] = 3
			case "duplicate groups":
				quality["duplicateEndpointPairs"] = 0
			case "duplicate node":
				nodes[1].(map[string]any)["id"] = node["id"]
			case "duplicate path":
				paths[1].(map[string]any)["id"] = path["id"]
			case "zero node ID":
				node["id"] = 0
			case "zero path ID":
				path["id"] = 0
			case "missing coordinate":
				delete(node, "x")
			case "null coordinate":
				node["x"] = nil
			case "missing map":
				delete(node, "mapId")
			case "missing name":
				delete(node, "name")
			case "missing map flag":
				delete(node, "mapReferenceKnown")
			case "missing price":
				delete(path, "baseCopper")
			case "missing source":
				delete(path, "source")
			case "missing endpoint flag":
				delete(path, "endpointsKnown")
			case "false endpoint flag":
				path["endpointsKnown"] = false
			case "invented endpoint flag":
				paths[2].(map[string]any)["endpointsKnown"] = true
			case "unknown field":
				node["offered"] = true
			case "negative price":
				path["baseCopper"] = -1
			}
			data, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if c, err := decodeCatalog(data); err == nil || c != nil {
				t.Fatal("malformed reference was accepted or returned a partial index", c, err)
			}
		})
	}
	valid := smallReferenceJSON(t)
	for name, data := range map[string][]byte{
		"truncated": valid[:len(valid)-1], "trailing document": append(append([]byte(nil), valid...), []byte(" {}")...),
		"invalid UTF8": append([]byte{0xff}, valid...), "coordinate overflow": bytes.Replace(valid, []byte(`"x":0`), []byte(`"x":1e9999`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if c, err := decodeCatalog(data); err == nil || c != nil {
				t.Fatal("malformed JSON or nonfinite coordinates were accepted", c, err)
			}
		})
	}
}

func TestReferenceGeneratorBoundaryChecks(t *testing.T) {
	// Keep the synthetic decoder and source-pin checks on the maintained Go CI
	// path. They need no asset download and must fail if Python is unavailable.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "python3", "-B", "generate_test.py").CombinedOutput()
	if err != nil {
		t.Fatalf("reference generator boundary checks failed: %v\n%s", err, output)
	}
}

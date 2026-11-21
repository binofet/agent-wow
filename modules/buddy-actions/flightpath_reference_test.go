package buddyactions

import (
	"context"
	"encoding/json"
	"net"
	"reflect"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/agent-wow/agent-wow/pkg/taxireference"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
)

func staticFlightpathReferenceService(t *testing.T) *Service {
	t.Helper()
	// An exact static read must also work without an initialized live body,
	// session callback or clock. Any World/transport dependency would fail it.
	s := NewService(nil, 0)
	s.now = func() time.Time {
		t.Fatal("static reference lookup consulted a live character clock")
		return time.Time{}
	}
	return s
}

func TestFlightpathReferenceProjectsAllCatalogFactsWithoutLiveDependencies(t *testing.T) {
	catalog, err := taxireference.Default()
	if err != nil {
		t.Fatal(err)
	}
	s := staticFlightpathReferenceService(t)
	knownNodes, paths := uint32(0), uint32(0)
	for id := uint32(1); id <= flightpathNodeLimit; id++ {
		view, err := s.FlightpathReference(context.Background(), &actionsv1.FlightpathReferenceRequest{NodeId: id})
		if err != nil || view == nil || !view.ReferenceOnly || view.LiveFareKnown || view.LiveRouteKnown || view.Info == nil {
			t.Fatal("static lookup acquired live authority or required session state", id, view, err)
		}
		want, found := catalog.Node(id)
		if view.NodeFound != found || (view.Node != nil) != found {
			t.Fatal("reference lookup invented or dropped an exact node", id, view)
		}
		if found {
			knownNodes++
			node := view.Node
			if node.NodeId != want.ID || node.MapId != want.MapID || node.X != want.X || node.Y != want.Y || node.Z != want.Z ||
				node.Name != want.Name || node.MapReferenceKnown != want.MapReferenceKnown {
				t.Fatal("RPC normalized a raw map, binary32 coordinate or French label", want, node)
			}
		}
		wantPaths := catalog.PathsFrom(id)
		if len(view.OutgoingPaths) != len(wantPaths) {
			t.Fatal("RPC filtered raw paths or collapsed duplicate endpoints", id, view.OutgoingPaths, wantPaths)
		}
		for i, want := range wantPaths {
			path := view.OutgoingPaths[i]
			if path.PathId != want.ID || path.SourceNode != want.Source || path.DestinationNode != want.Destination ||
				path.ReferenceBaseCopper != want.BaseCopper || !path.ReferencePriceKnown || path.EndpointsKnown != want.EndpointsKnown {
				t.Fatal("RPC changed a path identity, zero base price or endpoint fact", want, path)
			}
			paths++
		}
	}
	info := catalog.Info()
	// Node zero is deliberately invalid for this exact node-lookup RPC. Its raw
	// sentinel paths remain in Catalog; every positive source is checked above.
	if knownNodes != info.Quality.Nodes || paths+uint32(len(catalog.PathsFrom(0))) != info.Quality.Paths {
		t.Fatal("fixture did not exercise all catalog nodes and positive-source paths", knownNodes, paths, info.Quality)
	}
}

func TestFlightpathReferenceProtoJSONPreservesKnownZeroAndExactMetadataCasing(t *testing.T) {
	s := staticFlightpathReferenceService(t)
	view, err := s.FlightpathReference(context.Background(), &actionsv1.FlightpathReferenceRequest{NodeId: 175})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := protojson.Marshal(view) // Production default omission of scalar zero.
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["referenceOnly"] != true || wire["nodeFound"] != true || wire["liveFareKnown"] == true || wire["liveRouteKnown"] == true {
		t.Fatal("serialized reference facts advertised a live fare or route", wire)
	}
	duplicates := map[uint32]bool{}
	for _, value := range wire["outgoingPaths"].([]any) {
		path := value.(map[string]any)
		id := uint32(path["pathId"].(float64))
		if id == 733 || id == 793 {
			if _, present := path["referenceBaseCopper"]; present || path["referencePriceKnown"] != true ||
				path["endpointsKnown"] != true || path["sourceNode"] != float64(175) || path["destinationNode"] != float64(178) {
				t.Fatal("omitted numeric zero lost its independent knownness or duplicate identity", path)
			}
			duplicates[id] = true
		}
	}
	if len(duplicates) != 2 {
		t.Fatal("serialized output collapsed the two actual duplicate endpoint paths", duplicates)
	}
	catalog, err := taxireference.Default()
	if err != nil {
		t.Fatal(err)
	}
	info := catalog.Info()
	p, q := info.Provenance, info.Quality
	wantMetadata := map[string]any{
		"format": float64(info.Format), "claimedBuild": float64(info.ClaimedBuild), "nameLocale": info.NameLocale,
		"repository": p.Repository, "upstreamCommit": p.Commit, "nodesSha256": p.NodesSHA256,
		"pathsSha256": p.PathsSHA256, "mapSha256": p.MapSHA256, "nodeUrl": p.NodeURL, "pathUrl": p.PathURL, "mapSource": p.MapSource,
		"nodeCount": float64(q.Nodes), "pathCount": float64(q.Paths), "unnamedNodes": float64(q.UnnamedNodes), "unmappedNodes": float64(q.UnmappedNodes),
		"invalidEndpointPaths": float64(q.InvalidEndpointPaths), "duplicateEndpointPairs": float64(q.DuplicateEndpointPairs), "zeroPricePaths": float64(q.ZeroPricePaths),
	}
	if !reflect.DeepEqual(wire["info"], wantMetadata) {
		t.Fatal("ProtoJSON metadata casing or exact source provenance changed", wire["info"], wantMetadata)
	}
	var roundTrip actionsv1.FlightpathReferenceResponse
	if err := protojson.Unmarshal(raw, &roundTrip); err != nil || !proto.Equal(&roundTrip, view) {
		t.Fatal("default zero omission did not retain typed knownness on round trip", &roundTrip, err)
	}
}

func TestFlightpathReferenceKeepsUnknownBlankUnmappedAndInvalidRowsDistinct(t *testing.T) {
	s := staticFlightpathReferenceService(t)
	for _, id := range []uint32{216, 217, 220, 238, 353, 357} {
		view, err := s.FlightpathReference(context.Background(), &actionsv1.FlightpathReferenceRequest{NodeId: id})
		if err != nil || !view.NodeFound || view.Node == nil || view.Node.Name != "" {
			t.Fatal("known blank label became an unknown node or a guessed name", id, view, err)
		}
	}
	view, err := s.FlightpathReference(context.Background(), &actionsv1.FlightpathReferenceRequest{NodeId: 178})
	if err != nil || !view.NodeFound || view.Node.MapId != 3 || view.Node.MapReferenceKnown || view.Node.Name != "Transport, toundra Boréenne" {
		t.Fatal("raw unknown-map reference was filtered or became geometry authority", view, err)
	}
	for _, source := range []uint32{352, 353, 376, 377} {
		view, err := s.FlightpathReference(context.Background(), &actionsv1.FlightpathReferenceRequest{NodeId: source})
		if err != nil {
			t.Fatal(err)
		}
		invalid := 0
		for _, path := range view.OutgoingPaths {
			if path.DestinationNode == ^uint32(0) {
				invalid++
				if path.EndpointsKnown || path.ReferenceBaseCopper != 0 || !path.ReferencePriceKnown {
					t.Fatal("invalid endpoint or raw zero price was repaired", path)
				}
			}
		}
		if invalid == 0 {
			t.Fatal("invalid raw reference rows were hidden from the RPC", source, view)
		}
	}
	unknown, err := s.FlightpathReference(context.Background(), &actionsv1.FlightpathReferenceRequest{NodeId: ^uint32(0)})
	if err != nil || !unknown.ReferenceOnly || unknown.NodeFound || unknown.Node != nil || len(unknown.OutgoingPaths) != 0 || unknown.Info == nil ||
		unknown.LiveFareKnown || unknown.LiveRouteKnown {
		t.Fatal("unknown lookup invented fallback node, paths or live evidence", unknown, err)
	}
}

func TestFlightpathReferenceRepliesAreDetachedAndMalformedRequestsFail(t *testing.T) {
	s := staticFlightpathReferenceService(t)
	r := &actionsv1.FlightpathReferenceRequest{NodeId: 175}
	view, err := s.FlightpathReference(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	original := proto.Clone(view).(*actionsv1.FlightpathReferenceResponse)
	view.Node.Name, view.Node.X = "caller edit", 0
	view.Info.UpstreamCommit, view.Info.NodeCount = "caller edit", 0
	view.OutgoingPaths[0].PathId, view.OutgoingPaths[0].ReferenceBaseCopper = 0, 999
	fresh, err := s.FlightpathReference(context.Background(), r)
	if err != nil || !proto.Equal(fresh, original) {
		t.Fatal("reply mutation escaped into the shared static catalog", fresh, err)
	}
	for _, request := range []*actionsv1.FlightpathReferenceRequest{nil, {}} {
		if view, err := s.FlightpathReference(context.Background(), request); status.Code(err) != codes.InvalidArgument || view != nil {
			t.Fatal("malformed static request returned fallback facts", view, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if view, err := s.FlightpathReference(ctx, r); status.Code(err) != codes.Canceled || view != nil {
		t.Fatal("canceled reference call returned apparently current facts", view, err)
	}
}

func TestFlightpathReferencePublishedReadRoutesToStaticCompiledPrimitive(t *testing.T) {
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	actions, ok := registry.Lookup("buddy-actions")
	if !ok {
		t.Fatal("missing Actions release contract")
	}
	method, ok := actions.RPC("flightpath_reference")
	if !ok || method.Path() != "/warcraftbuddy.actions.v1.BuddyActions/FlightpathReference" ||
		actions.Manifest().ScriptAPI["flightpath_reference"].Effect != moddisc.EffectRead {
		t.Fatal("reference method lost its exact reflected read contract")
	}
	listener := bufconn.Listen(64 << 10)
	server := grpc.NewServer()
	actionsv1.RegisterBuddyActionsServer(server, staticFlightpathReferenceService(t))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///flightpath-reference", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	input, output := dynamicpb.NewMessage(method.Descriptor().Input()), dynamicpb.NewMessage(method.Descriptor().Output())
	if err := protojson.Unmarshal([]byte(`{"nodeId":175}`), input); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := conn.Invoke(ctx, method.Path(), input, output); err != nil {
		t.Fatal("published static read failed without live callbacks", err)
	}
	raw, err := protojson.Marshal(output)
	var view actionsv1.FlightpathReferenceResponse
	if err != nil || protojson.Unmarshal(raw, &view) != nil || !view.ReferenceOnly || !view.NodeFound || view.Node.GetNodeId() != 175 ||
		view.Info.GetNodeCount() != 364 || view.Info.GetPathCount() != 915 || view.LiveFareKnown || view.LiveRouteKnown {
		t.Fatal("shipped descriptor did not route the exact compiled reference contract", &view, err)
	}
}

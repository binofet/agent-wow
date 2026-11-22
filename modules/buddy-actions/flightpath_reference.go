package buddyactions

import (
	"context"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/taxireference"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// FlightpathReference has no session, World or transport dependency. It is an
// exact factual lookup; Lua owns destination and route choices over actual
// observed node knowledge. Reference prices are never realm fare quotations.
func (s *Service) FlightpathReference(ctx context.Context, request *actionsv1.FlightpathReferenceRequest) (*actionsv1.FlightpathReferenceResponse, error) {
	if request == nil || request.NodeId == 0 {
		return nil, status.Error(codes.InvalidArgument, "positive reference node ID required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	nodeID := request.NodeId
	catalog, err := taxireference.Default()
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "pinned flightpath reference unavailable: "+err.Error())
	}
	info := catalog.Info()
	q, p := info.Quality, info.Provenance
	out := &actionsv1.FlightpathReferenceResponse{ReferenceOnly: true, Info: &actionsv1.FlightpathReferenceInfo{
		Format: info.Format, ClaimedBuild: info.ClaimedBuild, NameLocale: info.NameLocale,
		Repository: p.Repository, UpstreamCommit: p.Commit, NodesSha256: p.NodesSHA256,
		PathsSha256: p.PathsSHA256, MapSha256: p.MapSHA256, NodeUrl: p.NodeURL, PathUrl: p.PathURL, MapSource: p.MapSource,
		NodeCount: q.Nodes, PathCount: q.Paths, UnnamedNodes: q.UnnamedNodes, UnmappedNodes: q.UnmappedNodes,
		InvalidEndpointPaths: q.InvalidEndpointPaths, DuplicateEndpointPairs: q.DuplicateEndpointPairs, ZeroPricePaths: q.ZeroPricePaths,
	}}
	if node, found := catalog.Node(nodeID); found {
		out.NodeFound = true
		out.Node = &actionsv1.FlightpathReferenceNode{NodeId: node.ID, MapId: node.MapID,
			X: node.X, Y: node.Y, Z: node.Z, Name: node.Name, MapReferenceKnown: node.MapReferenceKnown}
	}
	for _, path := range catalog.PathsFrom(nodeID) {
		out.OutgoingPaths = append(out.OutgoingPaths, &actionsv1.FlightpathReferencePath{PathId: path.ID,
			SourceNode: path.Source, DestinationNode: path.Destination, ReferenceBaseCopper: path.BaseCopper,
			ReferencePriceKnown: true, EndpointsKnown: path.EndpointsKnown})
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	return out, nil
}

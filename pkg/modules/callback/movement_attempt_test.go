package modcb

import (
	"context"
	"google.golang.org/grpc/metadata"
	"strings"
	"testing"
)

func TestMovementAttemptMetadataIsExactBoundedAndDiagnostic(t *testing.T) {
	id := strings.Repeat("ab", 32)
	ctx := WithMovementAttempt(context.Background(), id)
	md, _ := metadata.FromOutgoingContext(ctx)
	if IncomingMovementAttempt(metadata.NewIncomingContext(ctx, md)) != id {
		t.Fatal("attempt lost")
	}
	for _, v := range [][]string{{""}, {"a"}, {strings.Repeat("AB", 32)}, {strings.Repeat("a", 66)}, {id, id}} {
		if IncomingMovementAttempt(metadata.NewIncomingContext(context.Background(), metadata.MD{movementAttemptKey: v})) != "" {
			t.Fatal("accepted ambiguous/noncanonical metadata", v)
		}
	}
	if IncomingMovementAttempt(context.Background()) != "" {
		t.Fatal("invented attempt")
	}
}

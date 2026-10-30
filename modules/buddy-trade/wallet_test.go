package buddytrade

import (
	"context"
	"testing"

	"google.golang.org/protobuf/types/known/emptypb"
)

func TestStateExposesActualWorldWalletAndRejectsOlderRevision(t *testing.T) {
	s, f := fixture()
	f.world.SelfCopperKnown = true
	f.world.SelfCopper = 10000
	f.world.SelfCopperRevision = 2
	r, err := s.GetState(context.Background(), &emptypb.Empty{})
	if err != nil || !r.CopperKnown || r.Copper != 10000 || r.CopperRevision != 2 {
		t.Fatal(r, err)
	}
	f.world.SelfCopper = 9900
	f.world.SelfCopperRevision = 3
	r, err = s.GetState(context.Background(), nil)
	if err != nil || r.Copper != 9900 || r.CopperRevision != 3 {
		t.Fatal(r, err)
	}
	f.world.SelfCopper = 10000
	f.world.SelfCopperRevision = 2
	r, err = s.GetState(context.Background(), nil)
	if err != nil || r.Copper != 9900 || r.CopperRevision != 3 {
		t.Fatal("stale worldresponse regressed wallet", r, err)
	}
	r.Copper = 0
	if s.snapshot().Copper != 9900 {
		t.Fatal("caller mutated sharedwallet")
	}
}

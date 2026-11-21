package buddytrade

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type junkDispatchSession struct {
	*fakeSession
	calls        []string
	acquisitions []*actionsv1.InteractionRequest
	releases     []*actionsv1.InteractionRelease
	before       func(context.Context, string) error
	after        func(string, *modv1.InvokeModuleResponse) error
	beforeWrite  func(*modv1.SendPacketRequest)
}

func (f *junkDispatchSession) InvokeModule(ctx context.Context, request *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	method := request.Module + "." + request.Method
	f.calls = append(f.calls, method)
	if method == "buddy-actions.acquire_interaction" {
		var acquire actionsv1.InteractionRequest
		if err := request.Request.UnmarshalTo(&acquire); err != nil {
			return nil, err
		}
		f.acquisitions = append(f.acquisitions, &acquire)
	}
	if method == "buddy-actions.release_interaction" {
		var release actionsv1.InteractionRelease
		if err := request.Request.UnmarshalTo(&release); err != nil {
			return nil, err
		}
		f.releases = append(f.releases, &release)
	}
	if f.before != nil {
		if err := f.before(ctx, method); err != nil {
			return nil, err
		}
	}
	response, err := f.fakeSession.InvokeModule(ctx, request, opts...)
	if err == nil && f.after != nil {
		err = f.after(method, response)
	}
	return response, err
}

func (f *junkDispatchSession) SendPacket(ctx context.Context, request *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if f.beforeWrite != nil {
		f.beforeWrite(request)
	}
	f.calls = append(f.calls, fmt.Sprintf("packet:%d", request.Opcode))
	return f.fakeSession.SendPacket(ctx, request, opts...)
}

func junkDispatchFixture(carried bool) (*Service, *fakeSession, *junkDispatchSession, *tradev1.JunkRequest) {
	var s *Service
	var f *fakeSession
	var request *tradev1.JunkRequest
	if carried {
		s, f = carriedJunkFixture(4, 0)
		request = carriedJunkRequest(s, f, 4, 0)
	} else {
		s, f = junkFixture()
		request = requestJunk(s, f)
	}
	f.world.Self.MovementFlagsKnown = true
	f.world.Self.PositionKnown = true
	f.world.Self.PositionRevision = 7
	wrapper := &junkDispatchSession{fakeSession: f}
	s.session = wrapper
	return s, f, wrapper, request
}

func junkDispatchSource(f *fakeSession, carried bool) *worldv1.BackpackSlot {
	if carried {
		return f.world.InventoryBags[4].Slots[0]
	}
	return f.world.Backpack[2]
}

func TestJunkDispatchFinalWorldIsLastDependencyBeforeReservedWrite(t *testing.T) {
	for _, carried := range []bool{false, true} {
		t.Run(fmt.Sprint(carried), func(t *testing.T) {
			s, f, wrapper, request := junkDispatchFixture(carried)
			wrapper.beforeWrite = func(packet *modv1.SendPacketRequest) {
				if packet.Opcode != opcode.CMSGDestroyitem {
					return
				}
				if len(wrapper.calls) == 0 || wrapper.calls[len(wrapper.calls)-1] != "buddy-world.snapshot" {
					t.Fatalf("destroy followed a dependency after final World: %v", wrapper.calls)
				}
				if len(wrapper.acquisitions) != 2 || wrapper.acquisitions[0].Token != "" ||
					wrapper.acquisitions[1].Token != "lease-token" || wrapper.acquisitions[1].Owner != "trade" ||
					wrapper.acquisitions[1].LeaseMs != 30000 {
					t.Fatalf("exact existing lease was not renewed before final World: %v", wrapper.acquisitions)
				}
				if s.snapshot().LastJunkDisposal.GetItemGuid() != request.ExpectedItemGuid || f.count(opcode.CMSGDestroyitem) != 0 {
					t.Fatal("destroy was not reserved before transport")
				}
				want := []byte{255, 25, 0, 0, 0, 0}
				if carried {
					want = []byte{22, 0, 0, 0, 0, 0}
				}
				if !bytes.Equal(packet.Payload, want) {
					t.Fatalf("exact destroy packet changed: %x", packet.Payload)
				}
			}
			if got, err := s.DestroyJunk(context.Background(), request); err != nil || got.JunkStacksDestroyed != 0 || f.count(opcode.CMSGDestroyitem) != 1 {
				t.Fatal(got, err)
			}
			if f.actions.InteractionOwner != "" || f.actions.InteractionLeaseRemainingMs != 0 {
				t.Fatal("lease fixture mutated the shared pre-acquisition actions observation")
			}
		})
	}
}

func TestJunkDispatchRejectsWorldChangedByPostAcquisitionActionDependency(t *testing.T) {
	changes := map[string]func(*fakeSession, bool){
		"source_guid":  func(f *fakeSession, carried bool) { junkDispatchSource(f, carried).ItemGuid++ },
		"source_entry": func(f *fakeSession, carried bool) { junkDispatchSource(f, carried).Entry = 117 },
		"source_count": func(f *fakeSession, carried bool) { junkDispatchSource(f, carried).StackCount-- },
		"inventory_revision": func(f *fakeSession, carried bool) {
			if carried {
				f.world.InventoryRevision++
			} else {
				f.world.BackpackRevision++
			}
		},
		"combat":            func(f *fakeSession, _ bool) { f.world.Self.UnitFlags = 0x80000 },
		"motion":            func(f *fakeSession, _ bool) { f.world.Self.MovementFlags = 1 },
		"position_revision": func(f *fakeSession, _ bool) { f.world.Self.PositionRevision++ },
		"map":               func(f *fakeSession, _ bool) { f.world.Self.MapId++ },
		"map_unknown":       func(f *fakeSession, _ bool) { f.world.Self.MapKnown = false },
		"removed_body":      func(f *fakeSession, _ bool) { f.world.Self.Present = false },
	}
	for _, carried := range []bool{false, true} {
		for name, change := range changes {
			t.Run(fmt.Sprintf("%t/%s", carried, name), func(t *testing.T) {
				s, f, wrapper, request := junkDispatchFixture(carried)
				injected := false
				wrapper.before = func(_ context.Context, method string) error {
					postAcquire := method == "buddy-actions.status" && f.leases > 0 ||
						method == "buddy-actions.acquire_interaction" && len(wrapper.acquisitions) > 1
					if postAcquire && !injected {
						injected = true
						change(f, carried)
					}
					return nil
				}
				if got, err := s.DestroyJunk(context.Background(), request); !injected || err == nil || f.count(opcode.CMSGDestroyitem) != 0 || s.junkPending != nil {
					t.Fatalf("action dependency changed World but destruction was still admitted: injected=%t state=%v err=%v calls=%v", injected, got, err, wrapper.calls)
				}
			})
		}
	}
}

func rewriteJunkDispatchRenewal(wrapper *junkDispatchSession, change func(*actionsv1.InteractionLease)) func(string, *modv1.InvokeModuleResponse) error {
	return func(method string, response *modv1.InvokeModuleResponse) error {
		if method != "buddy-actions.acquire_interaction" || len(wrapper.acquisitions) != 2 {
			return nil
		}
		var lease actionsv1.InteractionLease
		if err := response.Result.UnmarshalTo(&lease); err != nil {
			return err
		}
		change(&lease)
		value, err := anypb.New(&lease)
		response.Result = value
		return err
	}
}

func TestJunkDispatchRejectsUnconfirmedOrUnsafeExactLeaseRenewal(t *testing.T) {
	changes := map[string]func(*actionsv1.InteractionLease){
		"missing_token":      func(l *actionsv1.InteractionLease) { l.Token = "" },
		"replacement_token":  func(l *actionsv1.InteractionLease) { l.Token = "another-token" },
		"missing_state":      func(l *actionsv1.InteractionLease) { l.State = nil },
		"missing_owner":      func(l *actionsv1.InteractionLease) { l.State.InteractionOwner = "" },
		"foreign_owner":      func(l *actionsv1.InteractionLease) { l.State.InteractionOwner = "loot" },
		"zero_ttl":           func(l *actionsv1.InteractionLease) { l.State.InteractionLeaseRemainingMs = 0 },
		"short_ttl":          func(l *actionsv1.InteractionLease) { l.State.InteractionLeaseRemainingMs = 2999 },
		"oversize_ttl":       func(l *actionsv1.InteractionLease) { l.State.InteractionLeaseRemainingMs = 30001 },
		"moving":             func(l *actionsv1.InteractionLease) { l.State.Moving = true },
		"motion_unconfirmed": func(l *actionsv1.InteractionLease) { l.State.MotionUnconfirmed = true },
		"casting":            func(l *actionsv1.InteractionLease) { l.State.CastPending = true },
		"attacking":          func(l *actionsv1.InteractionLease) { l.State.AutoAttacking = true },
		"attack_requested":   func(l *actionsv1.InteractionLease) { l.State.AutoAttackRequested = true },
		"attack_unconfirmed": func(l *actionsv1.InteractionLease) { l.State.AutoAttackUnconfirmed = true },
		"traveling":          func(l *actionsv1.InteractionLease) { l.State.Traveling = true },
	}
	for _, carried := range []bool{false, true} {
		for name, change := range changes {
			t.Run(fmt.Sprintf("%t/%s", carried, name), func(t *testing.T) {
				s, f, wrapper, request := junkDispatchFixture(carried)
				changed := false
				wrapper.after = rewriteJunkDispatchRenewal(wrapper, func(lease *actionsv1.InteractionLease) {
					changed = true
					change(lease)
				})
				if got, err := s.DestroyJunk(context.Background(), request); !changed || err == nil ||
					f.count(opcode.CMSGDestroyitem) != 0 || s.junkPending != nil {
					t.Fatal("unsafe renewal admitted destruction", got, err, changed)
				}
				if len(wrapper.releases) != 1 || wrapper.releases[0].Token != "lease-token" || wrapper.releases[0].Owner != "trade" {
					t.Fatal("failed renewal replaced custody of the original token", wrapper.releases)
				}
			})
		}
	}
}

func TestJunkDispatchValidLeaseBoundaryAndUnknownMotionPreserveNormalWrite(t *testing.T) {
	for _, carried := range []bool{false, true} {
		s, f, wrapper, request := junkDispatchFixture(carried)
		f.world.Self.MovementFlagsKnown = false
		f.world.Self.MovementFlags = 1 // An unknown flag value is not an observed motion proof.
		wrapper.after = rewriteJunkDispatchRenewal(wrapper, func(lease *actionsv1.InteractionLease) {
			lease.State.InteractionLeaseRemainingMs = 3000
		})
		if got, err := s.DestroyJunk(context.Background(), request); err != nil || got.JunkStacksDestroyed != 0 || f.count(opcode.CMSGDestroyitem) != 1 {
			t.Fatal("valid minimum lease or previously supported unknown motion was refused", got, err)
		}
	}
}

func TestJunkDispatchElapsedRenewalOrFinalWorldCannotOutliveLease(t *testing.T) {
	for _, carried := range []bool{false, true} {
		for _, delay := range []string{"renewal", "final_world"} {
			t.Run(fmt.Sprintf("%t/%s", carried, delay), func(t *testing.T) {
				s, f, wrapper, request := junkDispatchFixture(carried)
				now := time.Unix(100, 0)
				s.now = func() time.Time { return now }
				changed := false
				renewal := rewriteJunkDispatchRenewal(wrapper, func(lease *actionsv1.InteractionLease) {
					lease.State.InteractionLeaseRemainingMs = 3000
					if delay == "renewal" {
						now = now.Add(3100 * time.Millisecond)
						changed = true
					}
				})
				wrapper.after = func(method string, response *modv1.InvokeModuleResponse) error {
					if err := renewal(method, response); err != nil {
						return err
					}
					if delay == "final_world" && method == "buddy-world.snapshot" && len(wrapper.acquisitions) == 2 {
						now = now.Add(3100 * time.Millisecond)
						changed = true
					}
					return nil
				}
				if got, err := s.DestroyJunk(context.Background(), request); !changed || err == nil ||
					f.count(opcode.CMSGDestroyitem) != 0 || s.junkPending != nil {
					t.Fatal("elapsed dependency time did not consume lease validity", got, err, changed)
				}
			})
		}
	}
}

func TestJunkDispatchDependencyFailureOrCanceledContextNeverReservesOrWrites(t *testing.T) {
	for _, carried := range []bool{false, true} {
		for _, failure := range []string{"renewal_error", "world_error", "renewal_cancel", "world_cancel"} {
			t.Run(fmt.Sprintf("%t/%s", carried, failure), func(t *testing.T) {
				s, f, wrapper, request := junkDispatchFixture(carried)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				injected := false
				wrapper.before = func(call context.Context, method string) error {
					atRenewal := method == "buddy-actions.acquire_interaction" && len(wrapper.acquisitions) == 2
					atWorld := method == "buddy-world.snapshot" && len(wrapper.acquisitions) == 2
					if atRenewal || atWorld {
						deadline, bounded := call.Deadline()
						if !bounded || time.Until(deadline) > 3*time.Second {
							t.Fatal("final dispatch dependency lacked the three-second context bound")
						}
					}
					if atRenewal && (failure == "renewal_error" || failure == "renewal_cancel") ||
						atWorld && (failure == "world_error" || failure == "world_cancel") {
						injected = true
						if failure == "renewal_cancel" || failure == "world_cancel" {
							cancel() // The wrapped fake deliberately returns despite cancellation.
						} else {
							return errors.New("injected dependency unavailable")
						}
					}
					return nil
				}
				if got, err := s.DestroyJunk(ctx, request); !injected || err == nil || f.count(opcode.CMSGDestroyitem) != 0 || s.junkPending != nil {
					t.Fatal("failed or canceled dependency admitted destruction", got, err, injected)
				}
			})
		}
	}
}

func TestJunkDispatchRechecksRequestBindingAfterFinalWorldCallback(t *testing.T) {
	for _, carried := range []bool{false, true} {
		for _, field := range []string{"session", "revision"} {
			t.Run(fmt.Sprintf("%t/%s", carried, field), func(t *testing.T) {
				s, f, wrapper, request := junkDispatchFixture(carried)
				changed := false
				wrapper.after = func(method string, _ *modv1.InvokeModuleResponse) error {
					if method == "buddy-world.snapshot" && len(wrapper.acquisitions) == 2 {
						changed = true
						s.mu.Lock()
						if field == "session" {
							s.state.SessionId = "another-body"
						} else {
							s.state.Revision++
						}
						s.mu.Unlock()
					}
					return nil
				}
				if got, err := s.DestroyJunk(context.Background(), request); !changed || err == nil ||
					f.count(opcode.CMSGDestroyitem) != 0 || s.junkPending != nil {
					t.Fatal("late callback replaced the exact request binding", got, err, changed)
				}
			})
		}
	}
}

func TestJunkDispatchFinalWorldDoesNotStartUnrelatedMetadataQuery(t *testing.T) {
	for _, carried := range []bool{false, true} {
		t.Run(fmt.Sprint(carried), func(t *testing.T) {
			s, f, wrapper, request := junkDispatchFixture(carried)
			armed := false
			wrapper.after = rewriteJunkDispatchRenewal(wrapper, func(_ *actionsv1.InteractionLease) {
				armed = true
				// The selected stack is unchanged. An unrelated complete object is
				// visible in the final raw frame but has no cached template query.
				f.world.Backpack[0].Entry = 999999
				if carried {
					f.world.InventoryBags[0].Slots[0].Entry = 999999
				}
			})
			wrapper.beforeWrite = func(packet *modv1.SendPacketRequest) {
				if armed && packet.Opcode == opcode.CMSGItemQuerySingle {
					t.Fatal("final World invoked metadata traffic before the reserved destroy")
				}
				if packet.Opcode == opcode.CMSGDestroyitem && wrapper.calls[len(wrapper.calls)-1] != "buddy-world.snapshot" {
					t.Fatal("another dependency or metadata packet followed final World", wrapper.calls)
				}
			}
			if got, err := s.DestroyJunk(context.Background(), request); !armed || err != nil || f.count(opcode.CMSGDestroyitem) != 1 {
				t.Fatal(got, err, armed)
			}
			if s.queried[999999] {
				t.Fatal("unrelated final-frame metadata was queried by the final dispatch read")
			}
		})
	}
}

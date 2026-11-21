package buddytrade

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// These offsets follow the independently constructed normal server fixture,
// rather than the production reader or its private itemMetadata structure.
func rawTemplateFixture(id uint32, values [8]uint32) []byte {
	b := comparisonTemplate(id)
	prefix := 16 + len("Fixture Staff") + 4
	tail := len(b) - 29*4
	bonding := tail - len("Fixture description") - 1 - 4
	for i, at := range []int{prefix + 20*4, bonding, tail, tail + 3*4,
		tail + 4*4, tail + 11*4, tail + 14*4, tail + 26*4} {
		binary.LittleEndian.PutUint32(b[at:], values[i])
	}
	return b
}

func rawTemplateValues(facts *tradev1.RawTemplateFacts) [8]uint32 {
	return [8]uint32{facts.GetContainerSlots(), facts.GetBonding(), facts.GetPageText(), facts.GetStartQuest(),
		facts.GetLockId(), facts.GetMaxDurability(), facts.GetBagFamily(), facts.GetDuration()}
}

func requireRawTemplateRoundTrips(t *testing.T, item *tradev1.ItemDetails, want [8]uint32) {
	t.Helper()
	if !item.GetAvailable() || item.GetRawTemplateFacts() == nil || rawTemplateValues(item.RawTemplateFacts) != want {
		t.Fatalf("raw template facts missing or changed: %v, want %v", item, want)
	}
	wire, err := proto.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var decoded tradev1.ItemDetails
	if err := proto.Unmarshal(wire, &decoded); err != nil || decoded.RawTemplateFacts == nil || !proto.Equal(item, &decoded) {
		t.Fatalf("protobuf lost observed facts or presence: %v, %v", &decoded, err)
	}
	encoded, err := protojson.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var fromJSON tradev1.ItemDetails
	if err := protojson.Unmarshal(encoded, &fromJSON); err != nil || fromJSON.RawTemplateFacts == nil || !proto.Equal(item, &fromJSON) {
		t.Fatalf("JSON lost observed facts or presence: %s, %v", encoded, err)
	}
}

func TestRawTemplateFactsObservedZeroRetainsMessagePresence(t *testing.T) {
	item, err := parseLookupItem(rawTemplateFixture(905000, [8]uint32{}))
	if err != nil {
		t.Fatal(err)
	}
	requireRawTemplateRoundTrips(t, item, [8]uint32{})
	encoded, err := protojson.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, encoded); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(compact.Bytes(), []byte(`"rawTemplateFacts":{}`)) {
		t.Fatalf("observed zero facts became absent JSON coverage: %s", encoded)
	}
}

func TestRawTemplateFactsRetainEachSourceWordAndPermittedUpperBound(t *testing.T) {
	names := []string{"container_slots", "bonding", "page_text", "start_quest", "lock_id", "max_durability", "bag_family", "duration"}
	for i, name := range names {
		maximum := uint32(math.MaxUint32)
		if i == 1 {
			maximum = 5 // The existing normal metadata decoder bounds bonding.
		}
		for _, value := range []uint32{uint32(i + 1), maximum} {
			t.Run(fmt.Sprintf("%s/%d", name, value), func(t *testing.T) {
				var want [8]uint32
				want[i] = value
				item, err := parseLookupItem(rawTemplateFixture(905001, want))
				if err != nil {
					t.Fatal(err)
				}
				requireRawTemplateRoundTrips(t, item, want)
			})
		}
	}
	want := [8]uint32{20, 2, 300, 400, 500, 60, 700, 800}
	item, err := parseLookupItem(rawTemplateFixture(905001, want))
	if err != nil {
		t.Fatal(err)
	}
	requireRawTemplateRoundTrips(t, item, want)
}

func TestRawTemplateFactsIncompleteAndMalformedFramesNeverEstablishCoverage(t *testing.T) {
	const id = 905002
	full := rawTemplateFixture(id, [8]uint32{20, 2, 300, 400, 500, 60, 700, 800})
	s, _ := fixture()
	request := &tradev1.LookupItemsRequest{ItemIds: []uint32{id}}
	if _, err := s.LookupItems(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	for at := 0; at < len(full); at++ {
		item, err := parseLookupItem(full[:at])
		if err == nil || item.GetRawTemplateFacts() != nil {
			t.Fatalf("truncation %d established raw coverage: %v, %v", at, item, err)
		}
		packet(s, opcode.SMSGItemQuerySingleResponse, full[:at])
		if cached := s.lookup.items[id]; cached != nil {
			t.Fatalf("truncation %d entered requested cache: %v", at, cached)
		}
	}
	malformedFrames := map[string][]byte{
		"trailing_byte":   append(append([]byte(nil), full...), 0),
		"trailing_word":   append(append([]byte(nil), full...), u32(123)...),
		"invalid_bonding": rawTemplateFixture(id, [8]uint32{0, 6}),
	}
	for name, frame := range malformedFrames {
		t.Run(name, func(t *testing.T) {
			item, err := parseLookupItem(frame)
			if err == nil || item.GetRawTemplateFacts() != nil {
				t.Fatalf("malformed frame established raw coverage: %v, %v", item, err)
			}
			packet(s, opcode.SMSGItemQuerySingleResponse, frame)
			if cached := s.lookup.items[id]; cached != nil {
				t.Fatalf("malformed frame entered requested cache: %v", cached)
			}
		})
	}
	stillPending, err := s.LookupItems(context.Background(), request)
	if err != nil || !stillPending.Items[0].Pending || stillPending.Items[0].RawTemplateFacts != nil {
		t.Fatalf("malformed observations changed pending evidence: %v, %v", stillPending, err)
	}
}

func TestRawTemplateFactsUnknownStatesDoNotPromoteOmittedZeros(t *testing.T) {
	const id = 905003
	for _, mode := range []string{"pending", "unavailable", "transport_unconfirmed", "server_unconfirmed"} {
		t.Run(mode, func(t *testing.T) {
			s, f := fixture()
			now := time.Unix(1000, 0)
			s.now = func() time.Time { return now }
			if mode == "transport_unconfirmed" {
				f.fail = opcode.CMSGItemQuerySingle
			}
			request := &tradev1.LookupItemsRequest{ItemIds: []uint32{id}}
			result, err := s.LookupItems(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "unavailable":
				packet(s, opcode.SMSGItemQuerySingleResponse, u32(id|0x80000000))
				result, err = s.LookupItems(context.Background(), request)
			case "server_unconfirmed":
				now = now.Add(6 * time.Second)
				result, err = s.LookupItems(context.Background(), request)
			}
			if err != nil || result.Items[0].Available || result.Items[0].RawTemplateFacts != nil {
				t.Fatalf("unknown template became observed zero facts: %v, %v", result, err)
			}
			if (mode == "pending") != result.Items[0].Pending {
				t.Fatalf("unexpected pending state for %s: %v", mode, result)
			}
			encoded, err := protojson.Marshal(result.Items[0])
			if err != nil || bytes.Contains(encoded, []byte("rawTemplateFacts")) {
				t.Fatalf("unknown facts emitted coverage: %s, %v", encoded, err)
			}
		})
	}
}

func TestRawTemplateFactsRequireRequestedNormalReplyAndReturnDetachedNestedFacts(t *testing.T) {
	const id = 905004
	want := [8]uint32{20, 2, 300, 400, 500, 60, 700, 800}
	frame := rawTemplateFixture(id, want)
	s, f := fixture()
	packet(s, opcode.SMSGItemQuerySingleResponse, frame)
	if s.lookup.items[id] != nil {
		t.Fatal("unsolicited normal query established raw coverage")
	}
	request := &tradev1.LookupItemsRequest{ItemIds: []uint32{id}}
	before, err := s.LookupItems(context.Background(), request)
	if err != nil || before.Items[0].RawTemplateFacts != nil {
		t.Fatalf("request intention established raw coverage: %v, %v", before, err)
	}
	packet(s, opcode.SMSGItemQuerySingleResponse, frame)
	after, err := s.LookupItems(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	requireRawTemplateRoundTrips(t, after.Items[0], want)
	after.Items[0].RawTemplateFacts.Duration = 1
	after.Items[0].RawTemplateFacts.BagFamily = 2
	after.Items[0].RawTemplateFacts = &tradev1.RawTemplateFacts{PageText: 3}
	again, err := s.LookupItems(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	requireRawTemplateRoundTrips(t, again.Items[0], want)
	if again.Items[0].RawTemplateFacts == s.lookup.items[id].RawTemplateFacts || f.count(opcode.CMSGItemQuerySingle) != 1 {
		t.Fatal("nested cache alias or repeated item query")
	}
}

func TestRawTemplateFactsLegacyAvailableItemRemainsUnknownAcrossEncoding(t *testing.T) {
	legacy := &tradev1.ItemDetails{ItemId: 905005, Available: true, Name: "Legacy observed template"}
	wire, err := proto.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var binaryCopy tradev1.ItemDetails
	if err := proto.Unmarshal(wire, &binaryCopy); err != nil || !binaryCopy.Available || binaryCopy.RawTemplateFacts != nil {
		t.Fatalf("legacy protobuf fabricated raw coverage: %v, %v", &binaryCopy, err)
	}
	encoded, err := protojson.Marshal(legacy)
	if err != nil || bytes.Contains(encoded, []byte("rawTemplateFacts")) {
		t.Fatalf("legacy JSON fabricated raw coverage: %s, %v", encoded, err)
	}
	var jsonCopy tradev1.ItemDetails
	if err := protojson.Unmarshal(encoded, &jsonCopy); err != nil || !jsonCopy.Available || jsonCopy.RawTemplateFacts != nil {
		t.Fatalf("legacy JSON roundtrip fabricated raw coverage: %v, %v", &jsonCopy, err)
	}
}

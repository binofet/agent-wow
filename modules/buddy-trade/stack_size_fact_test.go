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
	"google.golang.org/protobuf/reflect/protoreflect"
)

func stackSizeTemplate(id, value uint32, raw [8]uint32) []byte {
	frame := rawTemplateFixture(id, raw)
	// The independent server fixture writes the signed Stackable word as raw
	// little-endian bits at prefix word19, immediately before ContainerSlots.
	prefix := 16 + len("Fixture Staff") + 4
	binary.LittleEndian.PutUint32(frame[prefix+19*4:], value)
	return frame
}

func checkStackSizePresence(t *testing.T, item *tradev1.ItemDetails, value uint32, raw [8]uint32) {
	t.Helper()
	if !item.GetAvailable() || item.RawTemplateFacts == nil || item.RawTemplateFacts.MaxStackCount == nil ||
		*item.RawTemplateFacts.MaxStackCount != value || rawTemplateValues(item.RawTemplateFacts) != raw {
		t.Fatalf("stack word or preceding raw facts changed: %v, want %d and %v", item, value, raw)
	}
}

func TestStackSizeFactPreservesRequestedRawWordAndEncodingPresence(t *testing.T) {
	raw := [8]uint32{20, 2, 300, 400, 500, 60, 700, 800}
	for _, value := range []uint32{0, 1, 20, 200, math.MaxInt32, 0x80000000, math.MaxUint32} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			const id = 906000
			s, f := fixture()
			request := &tradev1.LookupItemsRequest{ItemIds: []uint32{id}}
			before, err := s.LookupItems(context.Background(), request)
			if err != nil || before.Items[0].RawTemplateFacts != nil {
				t.Fatalf("query intention established stack coverage: %v, %v", before, err)
			}
			packet(s, opcode.SMSGItemQuerySingleResponse, stackSizeTemplate(id, value, raw))
			after, err := s.LookupItems(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			item := after.Items[0]
			checkStackSizePresence(t, item, value, raw)

			wire, err := proto.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			var fromWire tradev1.ItemDetails
			if err := proto.Unmarshal(wire, &fromWire); err != nil {
				t.Fatal(err)
			}
			checkStackSizePresence(t, &fromWire, value, raw)
			if !proto.Equal(item, &fromWire) {
				t.Fatal("protobuf changed the full observed template", item, &fromWire)
			}

			body, err := protojson.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatal(err)
			}
			var facts map[string]json.RawMessage
			if err := json.Unmarshal(decoded["rawTemplateFacts"], &facts); err != nil {
				t.Fatal(err)
			}
			word, present := facts["maxStackCount"]
			if !present || string(word) != fmt.Sprint(value) {
				t.Fatalf("JSON lost explicit stack word: %s", body)
			}
			var fromJSON tradev1.ItemDetails
			if err := protojson.Unmarshal(body, &fromJSON); err != nil {
				t.Fatal(err)
			}
			checkStackSizePresence(t, &fromJSON, value, raw)
			if !proto.Equal(item, &fromJSON) || f.count(opcode.CMSGItemQuerySingle) != 1 {
				t.Fatal("JSON changed the template or repeated its query", item, &fromJSON)
			}
		})
	}
}

func TestStackSizeFactLegacyRawMessageDoesNotInventKnownZero(t *testing.T) {
	old := &tradev1.ItemDetails{ItemId: 906001, Available: true, RawTemplateFacts: &tradev1.RawTemplateFacts{}}
	encoded, err := protojson.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, encoded); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(compact.Bytes(), []byte(`"rawTemplateFacts":{}`)) || bytes.Contains(encoded, []byte("maxStackCount")) {
		t.Fatalf("legacy eight-field coverage became a known zero stack word: %s", encoded)
	}
	var jsonCopy tradev1.ItemDetails
	if err := protojson.Unmarshal(encoded, &jsonCopy); err != nil || jsonCopy.RawTemplateFacts == nil ||
		jsonCopy.RawTemplateFacts.MaxStackCount != nil {
		t.Fatalf("legacy JSON fabricated stack coverage: %v, %v", &jsonCopy, err)
	}
	wire, err := proto.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	var wireCopy tradev1.ItemDetails
	if err := proto.Unmarshal(wire, &wireCopy); err != nil || wireCopy.RawTemplateFacts == nil ||
		wireCopy.RawTemplateFacts.MaxStackCount != nil {
		t.Fatalf("legacy protobuf fabricated stack coverage: %v, %v", &wireCopy, err)
	}
}

func TestStackSizeFactMalformedOrUnrequestedFramesCannotEstablishCoverage(t *testing.T) {
	const id = 906002
	full := stackSizeTemplate(id, math.MaxUint32, [8]uint32{})
	s, _ := fixture()
	packet(s, opcode.SMSGItemQuerySingleResponse, full)
	if s.lookup.items[id] != nil {
		t.Fatal("unsolicited reply established stack coverage")
	}
	request := &tradev1.LookupItemsRequest{ItemIds: []uint32{id}}
	if _, err := s.LookupItems(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	for at := 0; at < len(full); at++ {
		if item, err := parseLookupItem(full[:at]); err == nil || item.GetRawTemplateFacts() != nil {
			t.Fatalf("truncation %d established coverage: %v, %v", at, item, err)
		}
		packet(s, opcode.SMSGItemQuerySingleResponse, full[:at])
		if s.lookup.items[id] != nil {
			t.Fatalf("truncation %d entered requested cache", at)
		}
	}
	trailing := append(append([]byte(nil), full...), 0)
	if item, err := parseLookupItem(trailing); err == nil || item.GetRawTemplateFacts() != nil {
		t.Fatalf("trailing byte established coverage: %v, %v", item, err)
	}
	packet(s, opcode.SMSGItemQuerySingleResponse, trailing)
	if s.lookup.items[id] != nil {
		t.Fatal("trailing byte entered requested cache")
	}
	packet(s, opcode.SMSGItemQuerySingleResponse, u32(id|0x80000000))
	unavailable, err := s.LookupItems(context.Background(), request)
	if err != nil || unavailable.Items[0].Available || unavailable.Items[0].RawTemplateFacts != nil {
		t.Fatalf("negative server response fabricated stack coverage: %v, %v", unavailable, err)
	}

	for _, mode := range []string{"pending", "transport_unconfirmed", "server_unconfirmed"} {
		t.Run(mode, func(t *testing.T) {
			s, f := fixture()
			now := time.Unix(1000, 0)
			s.now = func() time.Time { return now }
			if mode == "transport_unconfirmed" {
				f.fail = opcode.CMSGItemQuerySingle
			}
			reply, err := s.LookupItems(context.Background(), request)
			if mode == "server_unconfirmed" && err == nil {
				now = now.Add(6 * time.Second)
				reply, err = s.LookupItems(context.Background(), request)
			}
			if err != nil || reply.Items[0].Available || reply.Items[0].RawTemplateFacts != nil {
				t.Fatalf("unknown query state fabricated stack coverage: %v, %v", reply, err)
			}
		})
	}
}

func TestStackSizeFactReturnedOptionalPointerIsDetachedFromCache(t *testing.T) {
	const id = 906003
	s, f := fixture()
	request := &tradev1.LookupItemsRequest{ItemIds: []uint32{id}}
	if _, err := s.LookupItems(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	raw := [8]uint32{8, 1, 2, 3, 4, 5, 6, 7}
	packet(s, opcode.SMSGItemQuerySingleResponse, stackSizeTemplate(id, 20, raw))
	first, err := s.LookupItems(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	checkStackSizePresence(t, first.Items[0], 20, raw)
	cached := s.lookup.items[id].RawTemplateFacts
	if first.Items[0].RawTemplateFacts == cached || first.Items[0].RawTemplateFacts.MaxStackCount == cached.MaxStackCount {
		t.Fatal("response aliases nested stack fact or optional pointer")
	}
	*first.Items[0].RawTemplateFacts.MaxStackCount = 0
	first.Items[0].RawTemplateFacts.Duration = 123
	next, err := s.LookupItems(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	checkStackSizePresence(t, next.Items[0], 20, raw)
	if f.count(opcode.CMSGItemQuerySingle) != 1 {
		t.Fatal("reading static stack facts repeated its query")
	}
}

func TestStackSizeFactDescriptorUsesOptionalUint32Presence(t *testing.T) {
	facts := &tradev1.RawTemplateFacts{}
	field := facts.ProtoReflect().Descriptor().Fields().ByNumber(9)
	if field == nil || field.Name() != "max_stack_count" || field.JSONName() != "maxStackCount" ||
		field.Kind() != protoreflect.Uint32Kind || !field.HasPresence() || field.IsList() || field.IsMap() ||
		field.ContainingOneof() == nil || !field.ContainingOneof().IsSynthetic() {
		t.Fatalf("stack fact is not a presence-bearing optional uint32: %v", field)
	}
	if facts.ProtoReflect().Has(field) {
		t.Fatal("legacy raw facts claim observed stack size")
	}
	facts.MaxStackCount = proto.Uint32(0)
	if !facts.ProtoReflect().Has(field) {
		t.Fatal("explicit observed zero lost presence")
	}
}

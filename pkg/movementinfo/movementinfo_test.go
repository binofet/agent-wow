package movementinfo_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/movementinfo"
)

func fullInfo() movementinfo.Info {
	return movementinfo.Info{
		Flags: 0x04201200, ExtraFlags: 0x400, Time: 0x11223344,
		X: 1.25, Y: -2.5, Z: 3.75, Orientation: math.Float32frombits(0x80000000),
		TransportPresent: true,
		Transport: movementinfo.Transport{
			GUID: 0x0100000000000002, X: 4, Y: 5, Z: 6, Orientation: 7,
			Time: 0x55667788, Seat: -1, Time2Present: true, Time2: math.MaxUint32,
		},
		PitchPresent: true, Pitch: 0.5, FallTime: 0xaabbccdd,
		JumpPresent: true, Jump: movementinfo.Jump{Velocity: 8, SinAngle: -0.25, CosAngle: 1, XYSpeed: 9},
		SplineElevationPresent: true, SplineElevation: 10,
	}
}

func TestMovementInfoFullWireLayout(t *testing.T) {
	// Fixed wire bytes establish field ordering independently of Encode.
	wire, err := hex.DecodeString(
		"00122004000444332211" + // flags, extra flags, timestamp
			"0000a03f000020c00000704000000080" + // own XYZO, including negative zero
			"810201" + // packed transport GUID, low/high bytes
			"000080400000a0400000c0400000e040" + // transport XYZO
			"88776655ffffffffff" + // transport time, signed seat-1, time2 MAX
			"0000003fddccbbaa" + // pitch, fallTime
			"00000041000080be0000803f00001041" + // jump
			"00002041") // spline elevation
	if err != nil {
		t.Fatal(err)
	}
	got, n, err := movementinfo.DecodePrefix(wire)
	if err != nil || n != len(wire) || !reflect.DeepEqual(got, fullInfo()) {
		t.Fatalf("decoded (%+v, %d, %v), want (%+v, %d, nil)", got, n, err, fullInfo(), len(wire))
	}
	if math.Float32bits(got.Orientation) != 0x80000000 {
		t.Fatal("negative zero orientation lost its wire bits")
	}
	encoded, err := movementinfo.Encode(got)
	if err != nil || !bytes.Equal(encoded, wire) {
		t.Fatalf("encoded %x, %v; want %x", encoded, err, wire)
	}
	// A CREATE update places nine speed floats and its spline after this prefix.
	withCreateTail := append(append([]byte(nil), wire...), make([]byte, 40)...)
	_, used, err := movementinfo.DecodePrefix(withCreateTail)
	if err != nil || used != len(wire) {
		t.Fatalf("consumed enclosing CREATE tail: %d, %v", used, err)
	}
}

func TestMovementInfoConditionalTailsAndEveryTruncation(t *testing.T) {
	pitches := []struct {
		flags uint32
		extra uint16
	}{{}, {flags: 0x00200000}, {flags: 0x02000000}, {extra: 0x20}, {flags: 0x02200000, extra: 0x20}}
	for transport := 0; transport < 3; transport++ {
		for pitch, mode := range pitches {
			for jump := 0; jump < 2; jump++ {
				for elevation := 0; elevation < 2; elevation++ {
					t.Run(fmt.Sprintf("transport%d_pitch%d_jump%d_elevation%d", transport, pitch, jump, elevation), func(t *testing.T) {
						i := movementinfo.Info{Flags: mode.flags, ExtraFlags: mode.extra, Time: 0, FallTime: math.MaxUint32}
						if transport != 0 {
							i.Flags |= 0x200
							i.TransportPresent = true
							i.Transport = fullInfo().Transport
							i.Transport.Time2Present = transport == 2
							if transport == 2 {
								i.ExtraFlags |= 0x400
							}
						}
						i.PitchPresent = pitch != 0
						i.Pitch = -0.75
						if jump != 0 {
							i.Flags |= 0x1000
							i.JumpPresent, i.Jump = true, fullInfo().Jump
						}
						if elevation != 0 {
							i.Flags |= 0x04000000
							i.SplineElevationPresent, i.SplineElevation = true, -19
						}
						wire, err := movementinfo.Encode(i)
						if err != nil {
							t.Fatal(err)
						}
						got, n, err := movementinfo.DecodePrefix(append(append([]byte(nil), wire...), 0xde, 0xad, 0xbe, 0xef))
						if err != nil || n != len(wire) {
							t.Fatalf("prefix consumed %d of %d: %v", n, len(wire), err)
						}
						again, err := movementinfo.Encode(got)
						if err != nil || !bytes.Equal(wire, again) {
							t.Fatalf("round trip %x != %x: %v", wire, again, err)
						}
						for end := 0; end < len(wire); end++ {
							partial, used, err := movementinfo.DecodePrefix(wire[:end])
							if !errors.Is(err, movementinfo.ErrTruncated) || used != 0 || partial != (movementinfo.Info{}) {
								t.Fatalf("truncation at %d accepted partial (%+v, %d, %v)", end, partial, used, err)
							}
						}
					})
				}
			}
		}
	}
}

func TestMovementInfoPackedGUIDZeroMaximumAndSignedSeat(t *testing.T) {
	for _, guid := range []uint64{0, 1, 0x8000000000000000, 0x0100000000000002, math.MaxUint64} {
		for _, seat := range []int8{-128, -1, 0, 127} {
			i := fullInfo()
			i.Transport.GUID, i.Transport.Seat = guid, seat
			i.Time, i.Transport.Time, i.Transport.Time2, i.FallTime = math.MaxUint32, math.MaxUint32, math.MaxUint32, math.MaxUint32
			wire, err := movementinfo.Encode(i)
			if err != nil {
				t.Fatal(err)
			}
			got, n, err := movementinfo.DecodePrefix(wire)
			if err != nil || n != len(wire) || !reflect.DeepEqual(got, i) {
				t.Fatalf("GUID %x seat%d overflow/loss: %+v, %d, %v", guid, seat, got, n, err)
			}
			if guid == math.MaxUint64 && len(wire) != movementinfo.MaxEncodedSize {
				t.Fatalf("maximum layout uses %d bytes, want %d", len(wire), movementinfo.MaxEncodedSize)
			}
		}
	}

	// Noncanonical mask bytes still carry an unambiguous GUID0. Encoding
	// canonicalizes only its packed representation, never its semantic value.
	i := fullInfo()
	i.Transport.GUID = 0
	canonical, err := movementinfo.Encode(i)
	if err != nil {
		t.Fatal(err)
	}
	redundant := append([]byte(nil), canonical[:26]...)
	redundant = append(redundant, 0xff, 0, 0, 0, 0, 0, 0, 0, 0)
	redundant = append(redundant, canonical[27:]...)
	got, n, err := movementinfo.DecodePrefix(redundant)
	if err != nil || n != len(redundant) || got.Transport.GUID != 0 {
		t.Fatalf("packed GUID0 not accepted: %+v, %d, %v", got, n, err)
	}
	again, err := movementinfo.Encode(got)
	if err != nil || !bytes.Equal(again, canonical) {
		t.Fatalf("packed GUID0 did not canonicalize: %x, %v", again, err)
	}
}

func TestMovementInfoUnknownFlagsAreFactsNotAdmission(t *testing.T) {
	for _, i := range []movementinfo.Info{
		{Flags: 0x80000000, ExtraFlags: 0x8000, X: math.MaxFloat32, Y: -math.MaxFloat32},
		{Flags: 0x70000000}, // existing passive-movement observation flags
		{ExtraFlags: 0x400}, // time2 is absent without the transport flag
		func() movementinfo.Info {
			i := fullInfo()
			i.Flags, i.ExtraFlags = math.MaxUint32, math.MaxUint16
			return i
		}(),
	} {
		wire, err := movementinfo.Encode(i)
		if err != nil {
			t.Fatal(err)
		}
		got, n, err := movementinfo.DecodePrefix(wire)
		if err != nil || n != len(wire) || !reflect.DeepEqual(got, i) || !got.AllFinite() {
			t.Fatalf("structural flags/finite coordinates filtered: %+v, %d, %v", got, n, err)
		}
	}
}

func TestMovementInfoPresentZeroFieldsRemainPresent(t *testing.T) {
	i := movementinfo.Info{
		Flags: 0x04201200, ExtraFlags: 0x400, TransportPresent: true,
		Transport:    movementinfo.Transport{Time2Present: true},
		PitchPresent: true, JumpPresent: true, SplineElevationPresent: true,
	}
	wire, err := movementinfo.Encode(i)
	if err != nil {
		t.Fatal(err)
	}
	got, n, err := movementinfo.DecodePrefix(wire)
	if err != nil || n != len(wire) || got != i {
		t.Fatalf("wire-present zero GUID/times/floats lost presence: %+v, %d, %v", got, n, err)
	}
	if wire[26] != 0 || len(wire) != movementinfo.MaxEncodedSize-8 {
		t.Fatalf("zero GUID did not use a single mask0 byte: %x", wire)
	}
}

func TestMovementInfoEncodeRejectsInconsistentPresence(t *testing.T) {
	changes := []func(*movementinfo.Info){
		func(i *movementinfo.Info) { i.TransportPresent = false },
		func(i *movementinfo.Info) { i.Flags &^= 0x200 },
		func(i *movementinfo.Info) { i.Transport.Time2Present = false },
		func(i *movementinfo.Info) { i.ExtraFlags &^= 0x400 },
		func(i *movementinfo.Info) { i.PitchPresent = false },
		func(i *movementinfo.Info) { i.Flags &^= 0x02200000 },
		func(i *movementinfo.Info) { i.JumpPresent = false },
		func(i *movementinfo.Info) { i.Flags &^= 0x1000 },
		func(i *movementinfo.Info) { i.SplineElevationPresent = false },
		func(i *movementinfo.Info) { i.Flags &^= 0x04000000 },
		func(i *movementinfo.Info) {
			*i = movementinfo.Info{Transport: movementinfo.Transport{Time2Present: true}}
		},
	}
	for n, change := range changes {
		i := fullInfo()
		change(&i)
		if wire, err := movementinfo.Encode(i); !errors.Is(err, movementinfo.ErrPresence) || wire != nil {
			t.Fatalf("inconsistent presence%d accepted: %x, %v", n, wire, err)
		}
	}
}

func TestMovementInfoNonfiniteBitsRemainStructural(t *testing.T) {
	fields := []struct {
		name string
		set  func(*movementinfo.Info, float32)
	}{
		{"x", func(i *movementinfo.Info, v float32) { i.X = v }},
		{"y", func(i *movementinfo.Info, v float32) { i.Y = v }},
		{"z", func(i *movementinfo.Info, v float32) { i.Z = v }},
		{"orientation", func(i *movementinfo.Info, v float32) { i.Orientation = v }},
		{"transport x", func(i *movementinfo.Info, v float32) { i.Transport.X = v }},
		{"transport y", func(i *movementinfo.Info, v float32) { i.Transport.Y = v }},
		{"transport z", func(i *movementinfo.Info, v float32) { i.Transport.Z = v }},
		{"transport orientation", func(i *movementinfo.Info, v float32) { i.Transport.Orientation = v }},
		{"pitch", func(i *movementinfo.Info, v float32) { i.Pitch = v }},
		{"jump velocity", func(i *movementinfo.Info, v float32) { i.Jump.Velocity = v }},
		{"jump sine", func(i *movementinfo.Info, v float32) { i.Jump.SinAngle = v }},
		{"jump cosine", func(i *movementinfo.Info, v float32) { i.Jump.CosAngle = v }},
		{"jump speed", func(i *movementinfo.Info, v float32) { i.Jump.XYSpeed = v }},
		{"spline elevation", func(i *movementinfo.Info, v float32) { i.SplineElevation = v }},
	}
	for _, field := range fields {
		for _, bits := range []uint32{0x7f800000, 0xff800000, 0x7fc12345, 0x7fa12345, 0xffc12345} {
			i := fullInfo()
			field.set(&i, math.Float32frombits(bits))
			if i.AllFinite() {
				t.Fatalf("%s nonfinite%x considered reusable", field.name, bits)
			}
			wire, err := movementinfo.Encode(i)
			if err != nil {
				t.Fatalf("structural encode rejected %s/%x: %v", field.name, bits, err)
			}
			got, n, err := movementinfo.DecodePrefix(wire)
			if err != nil || n != len(wire) || got.AllFinite() {
				t.Fatalf("structural decode of %s/%x: %d, %v", field.name, bits, n, err)
			}
			again, err := movementinfo.Encode(got)
			if err != nil || !bytes.Equal(wire, again) {
				t.Fatalf("%s/%x wire bits changed: %x != %x: %v", field.name, bits, wire, again, err)
			}
		}
	}

	// A value in a field absent from the wire is not a reusable fact and does
	// not affect finite checks; the structural presence flags remain separate.
	i := movementinfo.Info{Pitch: float32(math.Inf(1)), Jump: movementinfo.Jump{Velocity: float32(math.NaN())}}
	if !i.AllFinite() {
		t.Fatal("absent fields affected finite validation")
	}
}

func TestMovementInfoHasNoInputOrOutputAliases(t *testing.T) {
	wire, err := movementinfo.Encode(fullInfo())
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := movementinfo.DecodePrefix(wire)
	if err != nil {
		t.Fatal(err)
	}
	clear(wire)
	if !reflect.DeepEqual(got, fullInfo()) {
		t.Fatal("mutating input changed decoded facts")
	}
	encoded, err := movementinfo.Encode(got)
	if err != nil {
		t.Fatal(err)
	}
	clear(encoded)
	if !reflect.DeepEqual(got, fullInfo()) {
		t.Fatal("mutating output changed facts")
	}
}

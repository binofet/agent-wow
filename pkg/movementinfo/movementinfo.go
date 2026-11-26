// Package movementinfo encodes the structural build12340 MovementInfo wire
// layout. It does not establish movement authority or impose coordinate limits.
package movementinfo

import (
	"encoding/binary"
	"errors"
	"math"
)

// MaxEncodedSize includes every conditional tail and a GUID mask plus eight bytes.
const MaxEncodedSize = 88

var (
	ErrTruncated = errors.New("truncated MovementInfo")
	ErrPresence  = errors.New("MovementInfo flags and field presence disagree")
)

const (
	flagTransport       uint32 = 0x200
	flagFalling         uint32 = 0x1000
	flagPitch           uint32 = 0x02200000
	flagSplineElevation uint32 = 0x04000000
	extraPitch          uint16 = 0x20
	extraTransportTime2 uint16 = 0x400
)

// Info retains all MovementInfo fields, excluding any enclosing mover GUID or
// spline counter. Presence follows the wire flags, even for zero-valued fields.
// Float32 values retain their wire bits; decoding does not reject NaN or Inf.
type Info struct {
	Flags                  uint32
	ExtraFlags             uint16
	Time                   uint32
	X, Y, Z, Orientation   float32
	TransportPresent       bool
	Transport              Transport
	PitchPresent           bool
	Pitch                  float32
	FallTime               uint32
	JumpPresent            bool
	Jump                   Jump
	SplineElevationPresent bool
	SplineElevation        float32
}

// Transport contains the optional moving-transport tail. GUID zero and signed
// seat values are structurally valid; callers decide whether they are usable.
type Transport struct {
	GUID                 uint64
	X, Y, Z, Orientation float32
	Time                 uint32
	Seat                 int8
	Time2Present         bool
	Time2                uint32
}

// Jump contains the four-float falling tail that follows fallTime.
type Jump struct {
	Velocity, SinAngle, CosAngle, XYSpeed float32
}

// DecodePrefix decodes one complete MovementInfo and returns its byte length.
// Trailing bytes belong to the enclosing packet and are not consumed. On any
// truncation it returns a zero Info and zero length, never partial reusable data.
// The optional fields match the existing public 3.3.5a MovementInfo contract:
// transport, pitch, fallTime, jump, then spline elevation. Unknown flag bits are
// retained without being interpreted or rejected.
func DecodePrefix(b []byte) (Info, int, error) {
	c := reader{b: b}
	i := Info{Flags: c.u32(), ExtraFlags: c.u16(), Time: c.u32()}
	i.X, i.Y, i.Z, i.Orientation = c.f32(), c.f32(), c.f32(), c.f32()
	i.TransportPresent = i.Flags&flagTransport != 0
	if i.TransportPresent {
		i.Transport.GUID = c.guid()
		i.Transport.X, i.Transport.Y, i.Transport.Z, i.Transport.Orientation = c.f32(), c.f32(), c.f32(), c.f32()
		i.Transport.Time = c.u32()
		i.Transport.Seat = int8(c.u8())
		i.Transport.Time2Present = i.ExtraFlags&extraTransportTime2 != 0
		if i.Transport.Time2Present {
			i.Transport.Time2 = c.u32()
		}
	}
	i.PitchPresent = i.Flags&flagPitch != 0 || i.ExtraFlags&extraPitch != 0
	if i.PitchPresent {
		i.Pitch = c.f32()
	}
	i.FallTime = c.u32()
	i.JumpPresent = i.Flags&flagFalling != 0
	if i.JumpPresent {
		i.Jump = Jump{Velocity: c.f32(), SinAngle: c.f32(), CosAngle: c.f32(), XYSpeed: c.f32()}
	}
	i.SplineElevationPresent = i.Flags&flagSplineElevation != 0
	if i.SplineElevationPresent {
		i.SplineElevation = c.f32()
	}
	if c.short {
		return Info{}, 0, ErrTruncated
	}
	return i, c.off, nil
}

// Encode serializes every present field, preserving all float32 bits and flags.
// Presence must agree with the flags. Values in absent fields are not encoded.
// Packed transport GUIDs use the canonical mask (including mask0 for GUID0);
// redundant zero bytes in a decoded packed GUID need not be reproduced.
func Encode(i Info) ([]byte, error) {
	if i.TransportPresent != (i.Flags&flagTransport != 0) ||
		i.Transport.Time2Present != (i.TransportPresent && i.ExtraFlags&extraTransportTime2 != 0) ||
		i.PitchPresent != (i.Flags&flagPitch != 0 || i.ExtraFlags&extraPitch != 0) ||
		i.JumpPresent != (i.Flags&flagFalling != 0) ||
		i.SplineElevationPresent != (i.Flags&flagSplineElevation != 0) {
		return nil, ErrPresence
	}
	b := make([]byte, 0, MaxEncodedSize)
	b = binary.LittleEndian.AppendUint32(b, i.Flags)
	b = binary.LittleEndian.AppendUint16(b, i.ExtraFlags)
	b = binary.LittleEndian.AppendUint32(b, i.Time)
	b = appendFloats(b, i.X, i.Y, i.Z, i.Orientation)
	if i.TransportPresent {
		b = appendGUID(b, i.Transport.GUID)
		b = appendFloats(b, i.Transport.X, i.Transport.Y, i.Transport.Z, i.Transport.Orientation)
		b = binary.LittleEndian.AppendUint32(b, i.Transport.Time)
		b = append(b, byte(i.Transport.Seat))
		if i.Transport.Time2Present {
			b = binary.LittleEndian.AppendUint32(b, i.Transport.Time2)
		}
	}
	if i.PitchPresent {
		b = appendFloats(b, i.Pitch)
	}
	b = binary.LittleEndian.AppendUint32(b, i.FallTime)
	if i.JumpPresent {
		b = appendFloats(b, i.Jump.Velocity, i.Jump.SinAngle, i.Jump.CosAngle, i.Jump.XYSpeed)
	}
	if i.SplineElevationPresent {
		b = appendFloats(b, i.SplineElevation)
	}
	return b, nil
}

// AllFinite checks all wire-present floats, including optional tails, without a
// coordinate or gameplay bound. Structural decode/encode do not call it.
func (i Info) AllFinite() bool {
	if !finite(i.X, i.Y, i.Z, i.Orientation) {
		return false
	}
	if i.TransportPresent && !finite(i.Transport.X, i.Transport.Y, i.Transport.Z, i.Transport.Orientation) {
		return false
	}
	if i.PitchPresent && !finite(i.Pitch) {
		return false
	}
	if i.JumpPresent && !finite(i.Jump.Velocity, i.Jump.SinAngle, i.Jump.CosAngle, i.Jump.XYSpeed) {
		return false
	}
	return !i.SplineElevationPresent || finite(i.SplineElevation)
}

func finite(values ...float32) bool {
	for _, value := range values {
		if math.Float32bits(value)&0x7f800000 == 0x7f800000 {
			return false
		}
	}
	return true
}

func appendFloats(b []byte, values ...float32) []byte {
	for _, value := range values {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(value))
	}
	return b
}

func appendGUID(b []byte, guid uint64) []byte {
	maskAt := len(b)
	b = append(b, 0)
	for n := uint(0); n < 8; n++ {
		if value := byte(guid >> (8 * n)); value != 0 {
			b[maskAt] |= 1 << n
			b = append(b, value)
		}
	}
	return b
}

type reader struct {
	b     []byte
	off   int
	short bool
}

func (c *reader) take(n int) []byte {
	if c.short || n < 0 || n > len(c.b)-c.off {
		c.short = true
		return nil
	}
	b := c.b[c.off : c.off+n]
	c.off += n
	return b
}

func (c *reader) u8() byte {
	if b := c.take(1); b != nil {
		return b[0]
	}
	return 0
}

func (c *reader) u16() uint16 {
	if b := c.take(2); b != nil {
		return binary.LittleEndian.Uint16(b)
	}
	return 0
}

func (c *reader) u32() uint32 {
	if b := c.take(4); b != nil {
		return binary.LittleEndian.Uint32(b)
	}
	return 0
}

func (c *reader) f32() float32 { return math.Float32frombits(c.u32()) }

func (c *reader) guid() uint64 {
	mask := c.u8()
	var guid uint64
	for n := uint(0); n < 8; n++ {
		if mask&(1<<n) != 0 {
			guid |= uint64(c.u8()) << (8 * n)
		}
	}
	return guid
}

// SwimmingFlag adds pitch to ordinary swimming MovementInfo.
const SwimmingFlag uint32 = 0x00200000

// BodyWaterEntry is the surface-entry or stationary STOP image used after a
// measured ground-to-water fall. Neither can carry another movement mode.
func (i Info) BodyWaterEntry(forward bool) bool {
	flags := SwimmingFlag
	if forward {
		flags |= 1
	}
	return i.Flags == flags && i.ExtraFlags == 0 && i.PitchPresent && i.Pitch == 0 && i.FallTime == 0 && i.AllFinite()
}

// OwnFrame reads the entire normal MovementInfo, including its enclosing mover.
func OwnFrame(payload []byte, self uint64) (Info, bool) {
	c := reader{b: payload}
	if self == 0 || c.guid() != self || c.short {
		return Info{}, false
	}
	i, n, err := DecodePrefix(payload[c.off:])
	return i, err == nil && n == len(payload)-c.off && i.AllFinite()
}

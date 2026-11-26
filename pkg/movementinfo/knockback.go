package movementinfo

import (
	"encoding/binary"
	"errors"
	"math"
)

// Knockback is one actual 3.3.5 SMSG_MOVE_KNOCK_BACK order. The vertical
// value on the wire points down. A negative value launches the body upward.
type Knockback struct {
	Mover                          uint64
	Counter                        uint32
	Cos, Sin, Horizontal, Vertical float32
}

func DecodeKnockback(b []byte) (Knockback, error) {
	c := reader{b: b}
	k := Knockback{Mover: c.guid(), Counter: c.u32(), Cos: c.f32(), Sin: c.f32(), Horizontal: c.f32(), Vertical: c.f32()}
	if c.short || c.off != len(b) || k.Mover == 0 || !finite(k.Cos, k.Sin, k.Horizontal, k.Vertical) || k.Horizontal < 0 ||
		math.Abs(float64(k.Cos)*float64(k.Cos)+float64(k.Sin)*float64(k.Sin)-1) > .002 {
		return Knockback{}, errors.New("invalid full knockback order")
	}
	return k, nil
}

// Launch replaces voluntary movement with the realm's world-space impulse.
func (k Knockback) Launch(origin Info, clock uint32) Info {
	return Info{Flags: flagFalling, Time: clock, X: origin.X, Y: origin.Y, Z: origin.Z, Orientation: origin.Orientation,
		JumpPresent: true, Jump: Jump{Velocity: k.Vertical, SinAngle: k.Sin, CosAngle: k.Cos, XYSpeed: k.Horizontal}}
}

func (k Knockback) Acknowledgement(launch Info) []byte {
	b := binary.LittleEndian.AppendUint32(appendGUID(nil, k.Mover), k.Counter)
	info, _ := Encode(launch)
	return append(b, info...)
}

func (k Knockback) ReadAcknowledgement(b []byte) (Info, bool) {
	c := reader{b: b}
	if c.guid() != k.Mover || c.u32() != k.Counter || c.short {
		return Info{}, false
	}
	i, n, err := DecodePrefix(b[c.off:])
	return i, err == nil && n == len(b)-c.off && i.AllFinite() && i == k.Launch(i, i.Time)
}

// MoverFrame encodes the normal, counter-free heartbeat or physical contact.
func MoverFrame(self uint64, i Info) ([]byte, error) {
	info, err := Encode(i)
	if err != nil {
		return nil, err
	}
	return append(appendGUID(nil, self), info...), nil
}

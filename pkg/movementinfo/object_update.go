package movementinfo

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"
	"math"
)

var errObjectUpdate = errors.New("invalid object movement update")

// ObjectUpdatePayload unwraps one complete UPDATE_OBJECT payload. World and
// the immediate ROOT/UNROOT acknowledgement must agree on its record bounds.
func ObjectUpdatePayload(wire []byte, compressed bool) ([]byte, error) {
	const limit = 4 << 20
	if len(wire) < 4 || len(wire) > limit {
		return nil, errObjectUpdate
	}
	if !compressed {
		return wire, nil
	}
	size := binary.LittleEndian.Uint32(wire)
	if size < 4 || size > limit {
		return nil, errObjectUpdate
	}
	stream := bytes.NewReader(wire[4:])
	reader, err := zlib.NewReader(stream)
	if err != nil {
		return nil, errObjectUpdate
	}
	data, err := io.ReadAll(io.LimitReader(reader, int64(size)+1))
	closeErr := reader.Close()
	if err != nil || closeErr != nil || len(data) != int(size) || stream.Len() != 0 {
		return nil, errObjectUpdate
	}
	return data, nil
}

type ObjectMovement struct {
	Flags             uint16
	Living            Info
	LivingWire        []byte
	Speeds            [9]float32
	Spline            ObjectSpline
	Position          [4]float32
	AbsolutePosition  bool
	LowGUID, Progress uint32
	Rotation          uint64
}

type ObjectSpline struct {
	Flags, Elapsed, Duration, ID, Nodes uint32
	Mode                                byte
	Destination                         [3]float32
}

func (c *reader) objectFloat() float32 {
	f := c.f32()
	if !(math.Abs(float64(f)) < 1e6) {
		c.short = true
	}
	return f
}

func (c *reader) objectSpline() ObjectSpline {
	s := ObjectSpline{Flags: c.u32()}
	switch {
	case s.Flags&0x20000 != 0:
		c.objectFloat()
	case s.Flags&0x10000 != 0:
		c.take(8)
	case s.Flags&0x8000 != 0:
		for range 3 {
			c.objectFloat()
		}
	}
	s.Elapsed, s.Duration, s.ID = c.u32(), c.u32(), c.u32()
	for range 3 {
		c.objectFloat()
	}
	c.u32()
	s.Nodes = c.u32()
	if s.Nodes > 4096 || uint64(s.Nodes)*12 > uint64(len(c.b)-c.off) {
		c.short = true
		return s
	}
	for range s.Nodes * 3 {
		c.objectFloat()
	}
	s.Mode = c.u8()
	for i := range s.Destination {
		s.Destination[i] = c.objectFloat()
	}
	return s
}

// DecodeObjectMovementPrefix reads the movement part of a build-12340
// CREATE or MOVEMENT record. It leaves the update fields for the caller.
func DecodeObjectMovementPrefix(wire []byte) (ObjectMovement, int, error) {
	c := reader{b: wire}
	m := ObjectMovement{Flags: c.u16()}
	if m.Flags&^uint16(0x3ff) != 0 {
		return ObjectMovement{}, 0, errObjectUpdate
	}
	switch {
	case m.Flags&0x20 != 0:
		i, n, err := DecodePrefix(wire[c.off:])
		if err != nil {
			return ObjectMovement{}, 0, err
		}
		m.Living, m.LivingWire = i, c.take(n)
		for n := range m.Speeds {
			m.Speeds[n] = c.objectFloat()
			if m.Speeds[n] < 0 {
				return ObjectMovement{}, 0, errObjectUpdate
			}
		}
		if i.Flags&0x08000000 != 0 {
			m.Spline = c.objectSpline()
		}
	case m.Flags&0x100 != 0:
		m.AbsolutePosition = c.guid() == 0
		for i := range 3 {
			m.Position[i] = c.objectFloat()
		}
		c.take(12) // independently encoded transport offsets
		m.Position[3] = c.objectFloat()
		c.take(4)
	case m.Flags&0x40 != 0:
		m.AbsolutePosition = true
		for i := range m.Position {
			m.Position[i] = c.objectFloat()
		}
	}
	if m.Flags&0x08 != 0 {
		c.take(4)
	}
	if m.Flags&0x10 != 0 {
		m.LowGUID = c.u32()
	}
	if m.Flags&0x04 != 0 {
		c.guid()
	}
	if m.Flags&0x02 != 0 {
		m.Progress = c.u32()
	}
	if m.Flags&0x80 != 0 {
		c.take(8)
	}
	if m.Flags&0x200 != 0 {
		if b := c.take(8); b != nil {
			m.Rotation = binary.LittleEndian.Uint64(b)
		}
	}
	if c.short {
		return ObjectMovement{}, 0, errObjectUpdate
	}
	return m, c.off, nil
}

package buddynav

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
)

// TransportPath is the calculated cyclic path of a type-15 motion transport.
// It contains no clock, observed hull position, passenger or movement authority.
type TransportPath struct {
	frames                             []transportFrame
	period                             uint32
	speed, accel, accelTime, accelDist float32
}

type TransportPathSample struct {
	MapID    uint32
	Position Point
	Yaw      float32
	Stopped  bool
	// Transfer marks the receiving frame at the exact instant of a jump.
	Transfer bool
}

type transportNode struct {
	index, mapID, action, delay uint32
	point                       Point
}

type transportFrame struct {
	transportNode
	yaw                                    float32
	jump                                   bool
	curve                                  transportCurve
	length, since, until, timeFrom, timeTo float32
	arrival, departure                     uint32
}

// LoadTransportPath uses the copied client TaxiPathNode.dbc and the observed
// moving-transport template's path ID, move speed and acceleration. Its timing
// follows AzerothCore TransportMgr::GeneratePath and MotionTransport::CalculateSegmentPos.
// The caller must compare Period with the received GAMEOBJECT_LEVEL and bind
// samples to fresh, same-object/visit clock and position evidence.
func LoadTransportPath(ctx context.Context, dataDir string, pathID uint32, speed, accel float64) (*TransportPath, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	moveSpeed, acceleration := float32(speed), float32(accel)
	if dataDir == "" || pathID == 0 || !(moveSpeed > 0 && acceleration > 0) || !finite(moveSpeed) || !finite(acceleration) {
		return nil, errors.New("invalid moving transport path or template speed")
	}
	nodes, err := readTransportNodes(ctx, filepath.Join(dataDir, "dbc", "TaxiPathNode.dbc"), pathID)
	if err != nil {
		return nil, err
	}
	return newTransportPath(nodes, moveSpeed, acceleration)
}

func readTransportNodes(ctx context.Context, filename string, pathID uint32) ([]transportNode, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() {
		return nil, errors.New("TaxiPathNode.dbc is not a regular file")
	}
	reader := bufio.NewReader(f)
	var header [20]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	u32 := binary.LittleEndian.Uint32
	count, strings := u32(header[4:]), u32(header[16:])
	const recordBytes = 44
	if string(header[:4]) != "WDBC" || u32(header[8:]) != 11 || u32(header[12:]) != recordBytes || count == 0 ||
		int64(len(header))+int64(count)*recordBytes+int64(strings) != stat.Size() {
		return nil, errors.New("invalid TaxiPathNode.dbc layout")
	}
	var record [recordBytes]byte
	var nodes []transportNode
	for i := uint32(0); i < count; i++ {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if _, err := io.ReadFull(reader, record[:]); err != nil {
			return nil, err
		}
		if u32(record[4:]) != pathID {
			continue
		}
		p := Point{
			X: math.Float32frombits(u32(record[16:])),
			Y: math.Float32frombits(u32(record[20:])),
			Z: math.Float32frombits(u32(record[24:])),
		}
		if !finite(p.X) || !finite(p.Y) || !finite(p.Z) {
			return nil, errors.New("invalid transport path coordinate")
		}
		nodes = append(nodes, transportNode{
			index: u32(record[8:]), mapID: u32(record[12:]),
			point: p, action: u32(record[28:]), delay: u32(record[32:]),
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].index < nodes[j].index })
	for i, n := range nodes {
		if n.index != uint32(i) {
			return nil, errors.New("missing or duplicate transport node index")
		}
	}
	return nodes, nil
}

func newTransportPath(nodes []transportNode, speed, accel float32) (*TransportPath, error) {
	p := &TransportPath{speed: speed, accel: accel, accelTime: speed / accel, accelDist: 0.5 * speed * speed / accel}
	for i := 0; i < len(nodes); i++ {
		n := nodes[i]
		if i+1 < len(nodes) && (n.action&1 != 0 || n.mapID != nodes[i+1].mapID) {
			if len(p.frames) == 0 {
				return nil, errors.New("transport jump has no departure frame")
			}
			p.frames[len(p.frames)-1].jump = true
			i++ // both marker rows are outside the moving spline
			continue
		}
		f := transportFrame{transportNode: n}
		if i > 0 && i+1 < len(nodes) {
			f.yaw = transportYaw(nodes[i+1].point.X-nodes[i-1].point.X, nodes[i+1].point.Y-nodes[i-1].point.Y)
		}
		p.frames = append(p.frames, f)
	}
	// The first and last retained source rows are guide points.
	if len(p.frames) < 4 {
		return nil, errors.New("insufficient usable transport keyframes")
	}
	p.frames = p.frames[1 : len(p.frames)-1]
	p.frames[len(p.frames)-1].jump = true
	for first := 0; first < len(p.frames); {
		last := first
		for !p.frames[last].jump {
			last++
		}
		for i := first; i < last; i++ {
			a, b := p.frames[i].point, p.frames[i+1].point
			before, after := a, b
			before.X-- // noncyclic Catmull-Rom's virtual first control, orientation zero
			if i > first {
				before = p.frames[i-1].point
			}
			if i+1 < last {
				after = p.frames[i+2].point
			}
			f := &p.frames[i]
			f.curve = transportCurve{before, a, b, after}
			f.length = f.curve.length()
		}
		first = last + 1
	}
	if err := p.setTimes(); err != nil {
		return nil, err
	}
	return p, nil
}

// Period returns the calculated duration in milliseconds.
func (p *TransportPath) Period() uint32 {
	if p == nil {
		return 0
	}
	return p.period
}

func (p *TransportPath) setTimes() error {
	frames := p.frames
	n := len(frames)
	firstStop, lastStop := -1, 0
	for i := range frames {
		if frames[i].action == 2 {
			if firstStop < 0 {
				firstStop = i
			}
			lastStop = i
		}
	}
	if firstStop < 0 {
		firstStop = 0
	}
	// Distances wrap across map jumps, whose motion length is zero. Braking
	// and acceleration restart only at actual stop frames.
	var dist float32
	for k := 0; k < n; k++ {
		i := (lastStop + k) % n
		if frames[i].action == 2 || i == lastStop {
			dist = 0
		} else {
			dist += frames[(i+n-1)%n].length
		}
		frames[i].since = dist
	}
	dist = 0
	for k := n - 1; k >= 0; k-- {
		i := (firstStop + k) % n
		dist += frames[i].length
		frames[i].until = dist
		if frames[i].action == 2 || i == firstStop {
			dist = 0
		}
	}
	var legTime float32
	for k := 0; k < n; k++ {
		i := (lastStop + k) % n
		f := &frames[i]
		total := f.since + f.until
		switch {
		case total < 2*p.accelDist && f.since < f.until:
			f.timeTo = 2*transportSqrt(total/p.accel) - transportSqrt(2*f.since/p.accel)
		case total < 2*p.accelDist:
			f.timeTo = transportSqrt(2 * f.until / p.accel)
		case f.since < p.accelDist:
			f.timeTo = total/p.speed + p.accelTime - transportSqrt(2*f.since/p.accel)
		case f.until < p.accelDist:
			f.timeTo = transportSqrt(2 * f.until / p.accel)
		default:
			f.timeTo = f.until/p.speed + 0.5*p.speed/p.accel
		}
		if f.action == 2 || i == lastStop {
			legTime = f.timeTo
		}
		f.timeFrom = legTime - f.timeTo
	}
	var now float32
	// The wire period uses separately truncated millisecond boundaries after
	// accumulating the reference's single-precision seconds.
	for i := range frames {
		f := &frames[i]
		if i > 0 {
			now += frames[i-1].timeTo
			if f.action != 2 {
				now -= f.timeTo
			}
		}
		millis := now * 1000
		if !finite(millis) || millis < 0 || float64(millis) > math.MaxUint32 {
			return errors.New("invalid transport arrival time")
		}
		f.arrival = uint32(millis)
		if i > 0 && f.arrival < frames[i-1].departure {
			return errors.New("nonmonotonic transport path time")
		}
		if f.action == 2 {
			now += float32(f.delay)
		}
		millis = now * 1000
		if !finite(millis) || float64(millis) > math.MaxUint32 {
			return errors.New("invalid transport departure time")
		}
		f.departure = uint32(millis)
	}
	p.period = frames[n-1].departure
	if p.period == 0 {
		return errors.New("empty transport path duration")
	}
	return nil
}

func transportSqrt(x float32) float32 { return float32(math.Sqrt(float64(x))) }

func transportYaw(x, y float32) float32 {
	return float32(math.Mod(math.Atan2(float64(y), float64(x))+math.Pi, 2*math.Pi))
}

// Sample evaluates a supplied phase/elapsed path clock in milliseconds. It
// cannot establish a current clock or that a particular hull follows this path.
func (p *TransportPath) Sample(progressMs uint64) (TransportPathSample, error) {
	if p == nil || p.period == 0 {
		return TransportPathSample{}, errors.New("transport path unavailable")
	}
	t := uint32(progressMs % uint64(p.period))
	frames := p.frames
	i := sort.Search(len(frames), func(i int) bool { return frames[i].arrival > t }) - 1
	f := &frames[i]
	sample := TransportPathSample{MapID: f.mapID, Position: f.point, Yaw: f.yaw, Stopped: t < f.departure}
	if t == f.arrival {
		// Several zero-duration keyframes can share a transfer timestamp.
		for k := i; k >= 0 && frames[k].arrival == t; k-- {
			if frames[(k+len(frames)-1)%len(frames)].jump {
				sample.Transfer = true
				break
			}
		}
	}
	if sample.Stopped || sample.Transfer {
		// At a discontinuity the transport is placed at the receiving
		// keyframe's declared pose before the next spline update.
		return sample, nil
	}
	if f.jump || !(f.length > 0) {
		return TransportPathSample{}, fmt.Errorf("transport has no moving spline at phase %d", t)
	}
	elapsed := float32(t)*0.001 - float32(f.departure)*0.001
	since, until := f.timeFrom+elapsed, f.timeTo-elapsed
	dist := f.until - p.distanceFromStop(until)
	if since < until {
		dist = p.distanceFromStop(since) - f.since
	}
	u := dist / f.length
	if !finite(u) {
		return TransportPathSample{}, errors.New("invalid transport spline parameter")
	}
	sample.Position = f.curve.at(u, false)
	tangent := f.curve.at(u, true)
	sample.Yaw = transportYaw(tangent.X, tangent.Y)
	return sample, nil
}

// CrossesTransfer reports a discontinuity in (fromMs,toMs], including the lap
// boundary and same-map jumps. An unavailable path or reversed clock cannot
// establish continuity. A receiving-side checkpoint can start a new interval.
func (p *TransportPath) CrossesTransfer(fromMs, toMs uint64) bool {
	if p == nil || p.period == 0 || toMs < fromMs {
		return true
	}
	period := uint64(p.period)
	if fromMs/period != toMs/period {
		return true
	}
	from, to := uint32(fromMs%period), uint32(toMs%period)
	for i := 1; i < len(p.frames); i++ {
		t := p.frames[i].arrival
		if p.frames[i-1].jump && t > from && t <= to {
			return true
		}
	}
	return false
}

func (p *TransportPath) distanceFromStop(t float32) float32 {
	if t < p.accelTime {
		return 0.5 * p.accel * t * t
	}
	return p.accelDist + (t-p.accelTime)*p.speed
}

type transportCurve [4]Point

func (c transportCurve) at(t float32, derivative bool) Point {
	cubic, quadratic, linear, constant := t*t*t, t*t, t, float32(1)
	if derivative {
		cubic, quadratic, linear, constant = 3*t*t, 2*t, 1, 0
	}
	w0 := -0.5*cubic + quadratic - 0.5*linear
	w1 := 1.5*cubic - 2.5*quadratic + constant
	w2 := -1.5*cubic + 2*quadratic + 0.5*linear
	w3 := 0.5*cubic - 0.5*quadratic
	return Point{
		X: c[0].X*w0 + c[1].X*w1 + c[2].X*w2 + c[3].X*w3,
		Y: c[0].Y*w0 + c[1].Y*w1 + c[2].Y*w2 + c[3].Y*w3,
		Z: c[0].Z*w0 + c[1].Z*w1 + c[2].Z*w2 + c[3].Z*w3,
	}
}

func (c transportCurve) length() float32 {
	var length float64
	prev := c[1]
	for i := 1; i <= 3; i++ { // three subdivisions in build 12340's realm spline metric
		next := c.at(float32(i)/3, false)
		x, y, z := next.X-prev.X, next.Y-prev.Y, next.Z-prev.Z
		length += float64(transportSqrt(x*x + y*y + z*z))
		prev = next
	}
	return float32(length)
}

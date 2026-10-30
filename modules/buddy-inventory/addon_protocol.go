package buddyinventory

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
)

const addonPrefix = "WBINV1\t"

// AzerothCore SharedDefines.h: CHAT_MSG_PARTY=0x02, CHAT_MSG_SAY=0x01.
const addonPartyChannel = 2
const addonMaxParts = 64

type addonCommand struct {
	kind, nonce, snapshot, raw                             string
	revision                                               uint64
	sourceBag, sourceSlot, destinationBag, destinationSlot uint32
	itemGUID                                               uint64
}

func addonHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func addonNumber(s string, max uint64) (uint64, error) {
	n, e := strconv.ParseUint(s, 10, 64)
	if e != nil || n > max || strconv.FormatUint(n, 10) != s {
		return 0, errors.New("invalid addon number")
	}
	return n, nil
}

// Ordinary server PARTY/LANG_ADDON framing, authenticated by server sender GUID.
// Same framing as buddy-chat owner_feed.go; neither character names nor text
// supplied GUIDs grant authority. This parser never handles ordinary chat.
func decodeInventoryAddon(p []byte) (*addonCommand, bool, error) {
	if len(p) < 29+len(addonPrefix) || p[0] != addonPartyChannel || int32(binary.LittleEndian.Uint32(p[1:5])) != -1 || binary.LittleEndian.Uint64(p[5:13]) != 3 || !bytes.HasPrefix(p[29:], []byte(addonPrefix)) {
		return nil, false, nil
	}
	bad := errors.New("invalid inventory addon command")
	length := binary.LittleEndian.Uint32(p[25:29])
	if binary.LittleEndian.Uint32(p[13:17]) != 0 || binary.LittleEndian.Uint64(p[17:25]) != 0 || length < 10 || length > 256 || len(p) != 30+int(length) || p[28+int(length)] != 0 {
		return nil, true, bad
	}
	body := string(p[29+len(addonPrefix) : 28+int(length)])
	for _, c := range []byte(body) {
		if c < 0x20 || c > 0x7e {
			return nil, true, bad
		}
	}
	f := strings.Split(body, "|")
	if len(f) < 3 || f[0] != "1" || !addonHex(f[2], 12) {
		return nil, true, bad
	}
	out := &addonCommand{kind: f[1], nonce: f[2], raw: body}
	if out.kind == "R" {
		if len(f) != 3 {
			return nil, true, bad
		}
		return out, true, nil
	}
	expected := map[string]int{"M": 10, "E": 9, "G": 8, "J": 5}
	if expected[out.kind] != len(f) || !addonHex(f[3], 12) {
		return nil, true, bad
	}
	out.snapshot = f[3]
	var e error
	out.revision, e = addonNumber(f[4], ^uint64(0))
	if e != nil || out.revision == 0 {
		return nil, true, bad
	}
	if out.kind == "J" {
		return out, true, nil
	}
	b, e1 := addonNumber(f[5], 4)
	sl, e2 := addonNumber(f[6], 35)
	if e1 != nil || e2 != nil || !addonHex(f[7], 16) {
		return nil, true, bad
	}
	out.sourceBag, out.sourceSlot = uint32(b), uint32(sl)
	out.itemGUID, e = strconv.ParseUint(f[7], 16, 64)
	if e != nil || out.itemGUID>>48 != 0x4000 || uint32(out.itemGUID) == 0 {
		return nil, true, bad
	}
	if out.kind == "M" {
		b, e1 = addonNumber(f[8], 4)
		sl, e2 = addonNumber(f[9], 35)
		if e1 != nil || e2 != nil {
			return nil, true, bad
		}
		out.destinationBag, out.destinationSlot = uint32(b), uint32(sl)
	} else if out.kind == "E" {
		b, e1 = addonNumber(f[8], 4)
		if e1 != nil || b == 0 {
			return nil, true, bad
		}
		out.destinationBag = uint32(b)
	}
	return out, true, nil
}
func encodeInventoryAddon(body string) ([]byte, error) {
	text := addonPrefix + body
	if len(text) > 255 || len(body) == 0 {
		return nil, errors.New("inventory addon message exceeds bound")
	}
	for _, c := range []byte(body) {
		if c < 0x20 || c > 0x7e {
			return nil, errors.New("inventory addon message is not ASCII")
		}
	}
	out := make([]byte, 8+len(text)+1)
	binary.LittleEndian.PutUint32(out, addonPartyChannel)
	binary.LittleEndian.PutUint32(out[4:], 0xffffffff)
	copy(out[8:], text)
	return out, nil
}
func inventoryAddonParts(nonce, snapshot string, state *inventoryv1.State) ([]string, error) {
	if !addonHex(nonce, 12) || !addonHex(snapshot, 12) || state == nil || len(state.InventoryBags) > 5 {
		return nil, errors.New("invalid inventory addon snapshot")
	}
	var tokens []string
	bags := make(map[uint32]bool)
	total := 0
	for _, b := range state.InventoryBags {
		if b == nil || b.Bag > 4 || bags[b.Bag] || b.SlotCount > 36 {
			return nil, errors.New("invalid bag frame")
		}
		bags[b.Bag] = true
		known := state.InventoryKnown && b.SlotKnown && b.ContentsKnown
		for _, i := range b.Slots {
			if i == nil || i.ItemGuid != 0 && (!i.MetadataKnown || i.Entry == 0 || i.StackCount == 0) || i.ItemGuid == 0 && (i.MetadataKnown || i.Entry != 0 || i.StackCount != 0) {
				known = false
			}
		}
		if b.Bag > 0 && b.ContainerGuid == 0 {
			known = state.InventoryKnown && b.SlotKnown
		}
		k := 0
		if known {
			k = 1
		}
		tokens = append(tokens, fmt.Sprintf("B=%d,%d,%d,%d", b.Bag, b.SlotCount, b.Entry, k))
		if !known {
			continue
		}
		total += len(b.Slots)
		if total > 160 || len(b.Slots) != int(b.SlotCount) {
			return nil, errors.New("incomplete inventory snapshot")
		}
		seen := make(map[uint32]bool)
		for _, item := range b.Slots {
			if item == nil || item.Bag != b.Bag || item.Slot >= b.SlotCount || seen[item.Slot] {
				return nil, errors.New("invalid slot snapshot")
			}
			seen[item.Slot] = true
			if item.ItemGuid == 0 {
				continue
			}
			if !item.MetadataKnown || item.Entry == 0 || item.StackCount == 0 || item.ItemGuid>>48 != 0x4000 {
				return nil, errors.New("unknown occupied slot")
			}
			tokens = append(tokens, fmt.Sprintf("I=%d,%d,%d,%d,%016x", b.Bag, item.Slot, item.Entry, item.StackCount, item.ItemGuid))
		}
	}
	// Missing frames are explicitly unknown; omission never advertises emptiness.
	for i := uint32(0); i < 5; i++ {
		if !bags[i] {
			tokens = append(tokens, fmt.Sprintf("B=%d,0,0,0", i))
		}
	}
	var chunks []string
	for _, token := range tokens {
		if len(chunks) == 0 || len(chunks[len(chunks)-1])+1+len(token) > 170 {
			chunks = append(chunks, token)
		} else {
			chunks[len(chunks)-1] += ";" + token
		}
	}
	if len(chunks) > addonMaxParts {
		return nil, errors.New("inventory multipart bound exceeded")
	}
	out := make([]string, len(chunks))
	size := 0
	for i, chunk := range chunks {
		out[i] = fmt.Sprintf("1|S|%s|%s|%d|%d|%d|%s", nonce, snapshot, state.InventoryRevision, i+1, len(chunks), chunk)
		if _, e := encodeInventoryAddon(out[i]); e != nil {
			return nil, e
		}
		size += len(out[i])
	}
	if size > 16*1024 {
		return nil, errors.New("inventory multipart byte bound exceeded")
	}
	return out, nil
}

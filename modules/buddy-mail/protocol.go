// Package buddymail implements only normal mailbox listing and the explicitly
// authorized Training stipend money and exact Party bags attachment claims.
package buddymail

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"unicode/utf8"
)

const stipendCopper uint32 = 10000

type mailEntry struct {
	id, money, cod, stationery uint32
	sender                     uint64
	typeID, items              byte
	subject                    string
	attachments                []mailAttachment
}

type mailAttachment struct {
	index                 byte
	itemLow, entry, count uint32
}

func (m mailEntry) partyBags() bool {
	if m.id == 0 || m.typeID != 0 || m.sender != 0 || m.stationery != 61 || m.money != 0 || m.cod != 0 || m.subject != "Party bags" || m.items == 0 || m.items > 4 || len(m.attachments) != int(m.items) {
		return false
	}
	seen := make(map[uint32]bool)
	indices := make(map[byte]bool)
	for _, a := range m.attachments {
		if a.index >= m.items || indices[a.index] || a.itemLow == 0 || seen[a.itemLow] || a.entry != 41599 || a.count != 1 {
			return false
		}
		indices[a.index], seen[a.itemLow] = true, true
	}
	return true
}

func (m mailEntry) stipend() bool {
	return m.id != 0 && m.typeID == 0 && m.sender == 0 && m.stationery == 61 && m.money == stipendCopper && m.cod == 0 && m.items == 0 && m.subject == "Training stipend"
}

// Primary AzerothCore MailHandler.cpp SendMailList: uint32 total,uint8 count,
// then sized entries. Normal senders are raw uint64 GUIDs; other types uint32.
// The 118-byte item structure contains seven inspected enchantment slots.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/MailHandler.cpp
func decodeMailList(p []byte) ([]mailEntry, error) {
	bad := errors.New("malformed mail list")
	if len(p) < 5 || len(p) > 256*1024 || p[4] > 50 || binary.LittleEndian.Uint32(p[:4]) < uint32(p[4]) {
		return nil, bad
	}
	count, off := int(p[4]), 5
	result := make([]mailEntry, 0, count)
	seen := map[uint32]bool{}
	for range count {
		if off+2 > len(p) {
			return nil, bad
		}
		size := int(binary.LittleEndian.Uint16(p[off:]))
		if size < 40 || size > len(p)-off {
			return nil, bad
		}
		row := p[off : off+size]
		off += size
		m := mailEntry{id: binary.LittleEndian.Uint32(row[2:]), typeID: row[6]}
		if m.id == 0 || seen[m.id] {
			return nil, bad
		}
		seen[m.id] = true
		i := 7
		switch m.typeID {
		case 0:
			if i+8 > len(row) {
				return nil, bad
			}
			m.sender = binary.LittleEndian.Uint64(row[i:])
			i += 8
		case 2, 3, 4, 5:
			m.sender = uint64(binary.LittleEndian.Uint32(row[i:]))
			i += 4
		default:
			return nil, bad
		}
		if i+28 > len(row) {
			return nil, bad
		}
		m.cod = binary.LittleEndian.Uint32(row[i:])
		m.stationery = binary.LittleEndian.Uint32(row[i+8:])
		m.money = binary.LittleEndian.Uint32(row[i+12:])
		days := math.Float32frombits(binary.LittleEndian.Uint32(row[i+20:]))
		if math.IsNaN(float64(days)) || math.IsInf(float64(days), 0) || days < 0 {
			return nil, bad
		}
		i += 28
		for field := 0; field < 2; field++ {
			end := bytes.IndexByte(row[i:], 0)
			limit := 8000
			if field == 0 {
				limit = 255
			}
			if end < 0 || end > limit || !utf8.Valid(row[i:i+end]) {
				return nil, bad
			}
			if field == 0 {
				m.subject = string(row[i : i+end])
			}
			i += end + 1
		}
		if i >= len(row) {
			return nil, bad
		}
		m.items = row[i]
		i++
		if m.items > 12 || len(row)-i != int(m.items)*118 {
			return nil, bad
		}
		for n := 0; n < int(m.items); n++ {
			a := row[i+n*118 : i+(n+1)*118]
			m.attachments = append(m.attachments, mailAttachment{index: a[0], itemLow: binary.LittleEndian.Uint32(a[1:]), entry: binary.LittleEndian.Uint32(a[5:]), count: binary.LittleEndian.Uint32(a[101:])})
		}
		result = append(result, m)
	}
	if off != len(p) {
		return nil, bad
	}
	return result, nil
}

type mailResult struct{ id, action, result, equip, itemLow, count uint32 }

// Player::SendMailResult writes three uint32 words, optional equipment error or
// item GUID/count. MAIL_MONEY_TAKEN1/MAIL_OK0 alone is not wallet proof.
func decodeMailResult(p []byte) (mailResult, error) {
	bad := errors.New("malformed mail result")
	if len(p) < 12 {
		return mailResult{}, bad
	}
	r := mailResult{id: binary.LittleEndian.Uint32(p), action: binary.LittleEndian.Uint32(p[4:]), result: binary.LittleEndian.Uint32(p[8:])}
	want := 12
	if r.result == 1 {
		want = 16
	} else if r.action == 2 {
		want = 20
	}
	if r.action > 5 || len(p) != want {
		return mailResult{}, bad
	}
	if r.result == 1 {
		r.equip = binary.LittleEndian.Uint32(p[12:])
	} else if r.action == 2 {
		r.itemLow, r.count = binary.LittleEndian.Uint32(p[12:]), binary.LittleEndian.Uint32(p[16:])
	}
	return r, nil
}

func mailboxPacket(guid uint64) []byte {
	p := make([]byte, 8)
	binary.LittleEndian.PutUint64(p, guid)
	return p
}
func takeMoneyPacket(guid uint64, id uint32) []byte {
	p := make([]byte, 12)
	binary.LittleEndian.PutUint64(p, guid)
	binary.LittleEndian.PutUint32(p[8:], id)
	return p
}
func takeItemPacket(guid uint64, id, itemLow uint32) []byte {
	if guid == 0 || id == 0 || itemLow == 0 {
		return nil
	}
	p := make([]byte, 16)
	binary.LittleEndian.PutUint64(p, guid)
	binary.LittleEndian.PutUint32(p[8:], id)
	binary.LittleEndian.PutUint32(p[12:], itemLow)
	return p
}

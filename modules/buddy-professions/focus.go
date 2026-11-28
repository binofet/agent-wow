package buddyprofessions

import (
	"errors"

	"github.com/agent-wow/agent-wow/pkg/gameobjectquery"
)

type focusTemplate struct {
	entry, focusID, radius uint32
}

const focusMaxStringBytes = gameobjectquery.MaxStringBytes
const focusMaxPacketBytes = gameobjectquery.MaxPacketBytes

func focusQueryPacket(entry uint32, guid uint64) []byte {
	return gameobjectquery.Request(entry, guid)
}

// GameObjectTemplate::spellFocus has focusId at data0 and dist at data1.
func decodeFocusTemplate(p []byte) (focusTemplate, error) {
	m, err := gameobjectquery.Decode(p)
	if err != nil {
		return focusTemplate{}, err
	}
	if m.Type != 8 {
		return focusTemplate{}, errors.New("unsupported or unavailable spell focus template")
	}
	if m.Data[3] > 1 || m.Data[5] > 1 || m.Data[6] > 1 {
		return focusTemplate{}, errors.New("unsupported spell focus flags")
	}
	return focusTemplate{entry: m.Entry, focusID: m.Data[0], radius: m.Data[1]}, nil
}

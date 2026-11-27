// Package maps classifies maps and resolves map and zone IDs to stock WoW
// 3.3.5a (build 12340) English labels. Zone IDs refer to AreaTable entries,
// including subzones. The bundled data requires no client files or network
// access at runtime.
package maps

import (
	"slices"
	"strconv"
)

// DungeonMapForZone resolves the numeric party zone using its AreaTable map.
func DungeonMapForZone(zone uint32) (uint32, bool) {
	id, found := dungeonZoneMaps[zone]
	return id, found
}

// IsDungeon reports whether Map.dbc classifies a map as a party or raid
// instance. World maps, battlegrounds, arenas and unknown IDs return false.
func IsDungeon(id uint32) bool {
	_, found := slices.BinarySearch(dungeonMapIDs[:], id)
	return found
}

// MapName returns the English label for a map ID. Map 0 is Eastern Kingdoms.
// Unknown IDs return "Unknown map (<id>)" so custom IDs remain identifiable.
func MapName(id uint32) string {
	if name, ok := mapNames[id]; ok {
		return name
	}
	return "Unknown map (" + strconv.FormatUint(uint64(id), 10) + ")"
}

// ZoneName returns the English label for a zone or subzone ID.
// Unknown IDs, including 0, return "Unknown zone (<id>)".
func ZoneName(id uint32) string {
	if name, ok := zoneNames[id]; ok {
		return name
	}
	return "Unknown zone (" + strconv.FormatUint(uint64(id), 10) + ")"
}

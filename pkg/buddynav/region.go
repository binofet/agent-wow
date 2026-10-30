package buddynav

import "errors"

// Region selects a fixed, reviewed client-asset policy. Callers cannot supply
// arbitrary bounds, tiles or map IDs. The zero value retains Northshire.
type Region uint8

const (
	RegionNorthshire Region = iota
	// RegionDeathknellCrypt covers only the measured crypt stairs and nearby
	// surface endpoint. It does not cover the route to Shadow Priest Sarvis.
	RegionDeathknellCrypt
	// RegionColdridge covers only the starter valley around the dwarf/gnome
	// spawn and initial wolf-meat quest. It does not authorize Dun Morogh travel.
	RegionColdridge
	// RegionColdridgeTrollCave covers the measured Grelin-to-706 approach for
	// quest182, not the unobserved cave interior or other Coldridge goals.
	RegionColdridgeTrollCave
)

// Contains reports whether a point is inside one fixed regional policy.
func (r Region) Contains(p Point) bool { return r.contains(p) }

func (r Region) name() (string, error) {
	switch r {
	case RegionNorthshire:
		return "Northshire", nil
	case RegionDeathknellCrypt:
		return "Deathknell crypt", nil
	case RegionColdridge:
		return "Coldridge", nil
	case RegionColdridgeTrollCave:
		return "Coldridge troll approach", nil
	default:
		return "", errors.New("unknown navigation region")
	}
}

func (r Region) contains(p Point) bool {
	if !finite(p.X) || !finite(p.Y) || !finite(p.Z) {
		return false
	}
	switch r {
	case RegionNorthshire:
		return p.X >= -9599 && p.X <= -8400 && p.Y >= -700 && p.Y <= 200 && p.Z >= 40 && p.Z <= 180
	case RegionDeathknellCrypt:
		return p.X >= 1642 && p.X <= 1684 && p.Y >= 1662 && p.Y <= 1681 && p.Z >= 118 && p.Z <= 145
	case RegionColdridge:
		return p.X >= -6399 && p.X <= -6000 && p.Y >= 0 && p.Y <= 700 && p.Z >= 320 && p.Z <= 450
	case RegionColdridgeTrollCave:
		// Fresh public706 observations and both complete offline strict paths
		// are pinned in qualification-q182-20261030T2207. These bounds contain
		// their samples and reported destinations; every new route still needs
		// the unchanged physical sampler and fresh body/quest observations.
		return p.X >= -6452 && p.X <= -6355 && p.Y >= 525 && p.Y <= 570 && p.Z >= 383 && p.Z <= 389
	default:
		return false
	}
}

func (r Region) meshTiles() ([]string, error) {
	switch r {
	case RegionNorthshire:
		return []string{"0004731.mmtile", "0004732.mmtile", "0004733.mmtile", "0004831.mmtile", "0004832.mmtile", "0004833.mmtile", "0004931.mmtile", "0004932.mmtile", "0004933.mmtile"}, nil
	case RegionDeathknellCrypt:
		return []string{"0002828.mmtile"}, nil
	case RegionColdridge:
		return []string{"0004330.mmtile", "0004331.mmtile"}, nil
	case RegionColdridgeTrollCave:
		return []string{"0004330.mmtile", "0004331.mmtile", "0004430.mmtile", "0004431.mmtile"}, nil
	default:
		return nil, errors.New("unknown navigation region")
	}
}

func (r Region) allowsFloorTile(name string) bool {
	switch r {
	case RegionNorthshire:
		switch name {
		case "000_31_47.vmtile", "000_32_47.vmtile", "000_33_47.vmtile", "000_31_48.vmtile", "000_32_48.vmtile", "000_33_48.vmtile", "000_31_49.vmtile", "000_32_49.vmtile", "000_33_49.vmtile":
			return true
		}
	case RegionDeathknellCrypt:
		return name == "000_28_28.vmtile"
	case RegionColdridge:
		return name == "000_30_43.vmtile" || name == "000_31_43.vmtile"
	case RegionColdridgeTrollCave:
		return name == "000_30_43.vmtile" || name == "000_31_43.vmtile" || name == "000_30_44.vmtile" || name == "000_31_44.vmtile"
	}
	return false
}

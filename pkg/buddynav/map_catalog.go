package buddynav

import "slices"

// IDs from the installed build12340 enUS Map.dbc, WDBC135x66 fields.
// SHA256261bd7c2c9dbdba1f2966ca21106673ed722c2e3681d227f1eed261b6ce870d5.
// The public client table supplies map identity only. A listed ID still needs
// its actual installed mesh, terrain and collision data before any route.
var clientMapIDs = [...]uint32{
	0, 1, 13, 25, 30, 33, 34, 35, 36, 37, 42, 43, 44, 47, 48, 70, 90, 109,
	129, 169, 189, 209, 229, 230, 249, 269, 289, 309, 329, 349, 369, 389, 409,
	429, 449, 450, 451, 469, 489, 509, 529, 530, 531, 532, 533, 534, 540, 542,
	543, 544, 545, 546, 547, 548, 550, 552, 553, 554, 555, 556, 557, 558, 559,
	560, 562, 564, 565, 566, 568, 571, 572, 573, 574, 575, 576, 578, 580, 582,
	584, 585, 586, 587, 588, 589, 590, 591, 592, 593, 594, 595, 596, 597, 598,
	599, 600, 601, 602, 603, 604, 605, 606, 607, 608, 609, 610, 612, 613, 614,
	615, 616, 617, 618, 619, 620, 621, 622, 623, 624, 628, 631, 632, 641, 642,
	647, 649, 650, 658, 668, 672, 673, 712, 713, 718, 723, 724,
}

func clientMap(mapID uint32) bool { _, ok := slices.BinarySearch(clientMapIDs[:], mapID); return ok }

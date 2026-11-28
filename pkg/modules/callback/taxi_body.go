package modcb

// World attaches the XYZ of a current post-taxi physical origin to the exact
// incoming packet it parsed. The original physical observation revision is
// followed by three little-endian float32s.
const TaxiBodyOriginTrailer = "agentwow-taxi-body-origin-bin"

const TaxiBodyOriginSize = 8 + 3*4

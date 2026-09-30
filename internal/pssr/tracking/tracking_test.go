package tracking_test

import "pssrx/internal/pssr/tracking"

const (
	start    = int64(1_781_000_000_000_000_000)
	aroundNs = int64(4_040_000_000)
)

var testParams = tracking.Params{AroundTimeNs: aroundNs}

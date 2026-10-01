package upstreams

// NewLoaderForTest constructs a Loader with a fixed in-memory snapshot
// (no Postgres). Visible only to tests because the file ends in
// _test.go. Used by the unit tests for NewHealthHandler so we can
// exercise the handler without standing up the integration harness.
func NewLoaderForTest(cfgs ...UpstreamConfig) *Loader {
	l := &Loader{tier0Override: newTier0OverrideMap()}
	l.snap.Store(newSnapshot(cfgs))
	return l
}

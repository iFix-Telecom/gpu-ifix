package upstreams

// NewLoaderForTest constructs a Loader with a fixed in-memory snapshot
// (no Postgres). Visible only to tests because the file ends in
// _test.go. Used by the unit tests for NewHealthHandler so we can
// exercise the handler without standing up the integration harness.
func NewLoaderForTest(cfgs ...UpstreamConfig) *Loader {
	l := &Loader{}
	s := &snapshot{
		byName:     make(map[string]UpstreamConfig, len(cfgs)),
		byRoleTier: make(map[RoleTier]UpstreamConfig, len(cfgs)),
		ordered:    make([]UpstreamConfig, 0, len(cfgs)),
	}
	for _, u := range cfgs {
		s.byName[u.Name] = u
		s.byRoleTier[RoleTier{Role: u.Role, Tier: u.Tier}] = u
		s.ordered = append(s.ordered, u)
	}
	l.snap.Store(s)
	return l
}

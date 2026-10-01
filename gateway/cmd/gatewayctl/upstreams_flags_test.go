// Package main — quick 260930-uru: pure flag parsing tests for
// `gatewayctl upstreams update` (--url / --clear-url). No DB required.
package main

import (
	"testing"
)

func TestParseUpstreamsUpdateFlags(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCode int
		check    func(t *testing.T, o upstreamsUpdateOpts)
	}{
		{
			name:     "url and clear-url together",
			args:     []string{"--name=local-stt", "--url=http://1.2.3.4:5000", "--clear-url"},
			wantCode: 2,
		},
		{
			name:     "url with ftp scheme",
			args:     []string{"--name=local-stt", "--url=ftp://x"},
			wantCode: 2,
		},
		{
			name:     "url without scheme",
			args:     []string{"--name=local-stt", "--url=1.2.3.4:80"},
			wantCode: 2,
		},
		{
			name:     "missing name",
			args:     []string{"--url=http://1.2.3.4:5000"},
			wantCode: 2,
		},
		{
			name:     "valid url alone",
			args:     []string{"--name=local-stt", "--url=http://1.2.3.4:5000"},
			wantCode: 0,
			check: func(t *testing.T, o upstreamsUpdateOpts) {
				if o.url != "http://1.2.3.4:5000" || o.clearURL {
					t.Fatalf("opts = %+v", o)
				}
				if !o.hasURLChange() || o.hasAdminChange() {
					t.Fatalf("url-only update must be a URL change without admin change: %+v", o)
				}
			},
		},
		{
			name:     "clear-url alone",
			args:     []string{"--name=embed-gpu", "--clear-url"},
			wantCode: 0,
			check: func(t *testing.T, o upstreamsUpdateOpts) {
				if !o.clearURL || o.url != "" || !o.hasURLChange() || o.hasAdminChange() {
					t.Fatalf("opts = %+v", o)
				}
			},
		},
		{
			name:     "admin flags keep working",
			args:     []string{"--name=local-llm", "--enabled=false", "--tier=1"},
			wantCode: 0,
			check: func(t *testing.T, o upstreamsUpdateOpts) {
				if !o.hasAdminChange() || o.hasURLChange() {
					t.Fatalf("opts = %+v", o)
				}
			},
		},
		{
			name:     "invalid enabled",
			args:     []string{"--name=local-llm", "--enabled=maybe"},
			wantCode: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, code := parseUpstreamsUpdateFlags(tc.args)
			if code != tc.wantCode {
				t.Fatalf("code = %d, want %d (opts=%+v)", code, tc.wantCode, o)
			}
			if tc.check != nil {
				tc.check(t, o)
			}
		})
	}
}

package alert

// testsupport.go — shared recording fakes for the alert package's three
// delivery channels. This file is intentionally build-tag-free: it is
// production-adjacent test support (the in-repo analog of the project's
// `__test-helpers__` convention) so that BOTH plan 07-04's concrete-client
// tests and plan 07-05's alerter tests can import the same doubles without
// a `_test.go` visibility wall or a duplicate definition.
//
// The fakes deliberately do NOT depend on the Channel interface or the
// Message struct — those are defined later by plan 07-04 in client.go.
// Each fake exposes a Send method with the same (title, body) call shape
// the concrete clients receive, records every call, and returns its
// configurable Err. When 07-04 lands the Channel interface, a thin adapter
// (or a direct method-set match if Channel's Send is widened) lets these
// fakes stand in for the real clients; until then they are usable as plain
// recording spies.
//
// All three fakes are safe for the single-goroutine alerter usage pattern;
// they are NOT mutex-guarded because the alerter fans out from one
// goroutine. Tests that exercise concurrency should add their own
// synchronization.

import "context"

// FakeCall is one recorded Send invocation. Tests assert against the
// slice of these to verify the alerter delivered the expected payloads.
type FakeCall struct {
	Title string
	Body  string
}

// FakeChatwoot is a recording double for the Chatwoot Application API
// client. Sent accumulates the formatted "Title\nBody" string of every
// Send call (matching the plan's `FakeChatwoot{ Sent []string }` shape);
// Calls keeps the structured (Title, Body) pairs for finer assertions.
// Err, when non-nil, is returned by every Send call so tests can drive
// the breaker-open / retry paths.
type FakeChatwoot struct {
	Sent  []string
	Calls []FakeCall
	Err   error
}

// Name identifies the channel — matches the Channel.Name() contract that
// plan 07-04 defines so this fake slots in without change.
func (f *FakeChatwoot) Name() string { return "chatwoot" }

// Send records the call and returns the configured Err (nil = success).
func (f *FakeChatwoot) Send(_ context.Context, title, body string) error {
	f.Sent = append(f.Sent, title+"\n"+body)
	f.Calls = append(f.Calls, FakeCall{Title: title, Body: body})
	return f.Err
}

// Reset clears the recorded calls (handy between table-test cases).
func (f *FakeChatwoot) Reset() {
	f.Sent = nil
	f.Calls = nil
}

// FakeClickUp is a recording double for the ClickUp task-creation client.
type FakeClickUp struct {
	Sent  []string
	Calls []FakeCall
	Err   error
}

// Name identifies the channel.
func (f *FakeClickUp) Name() string { return "clickup" }

// Send records the call and returns the configured Err (nil = success).
func (f *FakeClickUp) Send(_ context.Context, title, body string) error {
	f.Sent = append(f.Sent, title+"\n"+body)
	f.Calls = append(f.Calls, FakeCall{Title: title, Body: body})
	return f.Err
}

// Reset clears the recorded calls.
func (f *FakeClickUp) Reset() {
	f.Sent = nil
	f.Calls = nil
}

// FakeBrevo is a recording double for the Brevo SMTP email client.
type FakeBrevo struct {
	Sent  []string
	Calls []FakeCall
	Err   error
}

// Name identifies the channel.
func (f *FakeBrevo) Name() string { return "brevo" }

// Send records the call and returns the configured Err (nil = success).
func (f *FakeBrevo) Send(_ context.Context, title, body string) error {
	f.Sent = append(f.Sent, title+"\n"+body)
	f.Calls = append(f.Calls, FakeCall{Title: title, Body: body})
	return f.Err
}

// Reset clears the recorded calls.
func (f *FakeBrevo) Reset() {
	f.Sent = nil
	f.Calls = nil
}

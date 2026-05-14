package alert

// brevo.go — the Brevo SMTP alert channel (critical + warning email,
// OBS-05). CONTEXT.md locks "Brevo SMTP" explicitly, so this uses Go's
// net/smtp against the Brevo relay (typically host:587) — NOT the Brevo
// transactional HTTP API.
//
// # Transport security (WR-05 — accurate posture)
//
// Submission goes through net/smtp.SendMail, which does OPPORTUNISTIC
// STARTTLS: it upgrades to TLS only if the relay advertises STARTTLS.
// This code does NOT construct an explicit tls.Config, does NOT pin a
// ServerName, and does NOT enforce port 587 or reject a plaintext relay.
//
// What this DOES guarantee: the auth step is smtp.PlainAuth, which
// refuses to transmit the password unless the connection is already TLS
// (or the host is localhost). So a MITM that strips the relay's STARTTLS
// capability advertisement does NOT leak the credential — PlainAuth's
// own guard fails the send with "unencrypted connection" instead. The
// failure mode of a stripped-capability MITM is therefore a SILENTLY
// FAILED ALERT, not a leaked password. That is acceptable for
// secret-safety (T-07-11) but it is a degraded-availability outcome, not
// a verified-encryption guarantee. If a future requirement needs the
// latter, build the SMTP conversation explicitly with
// tls.Config{ServerName: c.host} and require STARTTLS so a stripped
// advertisement is a hard, observable error.
//
// Email is the least latency-sensitive channel, so the send is wrapped
// in BOTH a per-service gobreaker (fast-fail when the relay is dead)
// AND a short backoff retry (a transient submission hiccup gets 2-3
// tries). Credentials (the SMTP user/pass) live in the struct and touch
// smtp.PlainAuth in exactly one method — they never reach a log or an
// error string.

import (
	"context"
	"fmt"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// brevoBreakerFailures is the consecutive-failure count that trips the
// Brevo circuit breaker. Matches breaker.DefaultOptions (D-A3).
const brevoBreakerFailures = 3

// brevoMaxRetries is the backoff attempt budget for a single send.
// Email tolerates latency, so a transient relay hiccup gets a few
// tries — but bounded, so a hard-down relay does not loop. The gobreaker
// in front of the retry is what actually fast-fails a sustained outage.
const brevoMaxRetries = 3

// brevoInitialBackoff is the first retry interval. Kept short — the
// breaker, not the retry budget, is the outage backstop.
const brevoInitialBackoff = 200 * time.Millisecond

// BrevoConfig is the subset of config.Config the Brevo client needs.
type BrevoConfig struct {
	Host string   // BREVO_SMTP_HOST, e.g. smtp-relay.brevo.com
	Port int      // BREVO_SMTP_PORT (587)
	User string   // BREVO_SMTP_USER
	Pass string   // BREVO_SMTP_PASS
	From string   // ALERT_EMAIL_FROM
	To   []string // ALERT_EMAIL_TO
}

// BrevoClient is the Brevo SMTP alert channel. Construct via
// NewBrevoClient. Safe for concurrent use — the gobreaker is
// goroutine-safe and smtp.SendMail opens a fresh connection per call.
type BrevoClient struct {
	host string
	port int
	user string
	pass string
	from string
	to   []string
	cb   *gobreaker.CircuitBreaker[struct{}]
	// sendMail is the SMTP submission func, injected so tests can drive
	// the success / failure / breaker-open paths without a live relay.
	// Defaults to net/smtp.SendMail in NewBrevoClient.
	sendMail func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

// compile-time assertion: BrevoClient implements Channel.
var _ Channel = (*BrevoClient)(nil)

// NewBrevoClient wires a BrevoClient with its own "brevo" circuit
// breaker and the real net/smtp.SendMail. Credentials are held in the
// struct only for signing PlainAuth — they are never logged.
func NewBrevoClient(cfg BrevoConfig) *BrevoClient {
	return &BrevoClient{
		host:     cfg.Host,
		port:     cfg.Port,
		user:     cfg.User,
		pass:     cfg.Pass,
		from:     cfg.From,
		to:       append([]string(nil), cfg.To...),
		cb:       newBrevoBreaker(),
		sendMail: smtp.SendMail,
	}
}

// newBrevoBreaker builds the per-service circuit breaker. One breaker
// per external service so a dead Brevo relay opens its own breaker
// without affecting Chatwoot or ClickUp (T-07-12).
func newBrevoBreaker() *gobreaker.CircuitBreaker[struct{}] {
	return gobreaker.NewCircuitBreaker[struct{}](gobreaker.Settings{
		Name: "brevo",
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.ConsecutiveFailures >= brevoBreakerFailures
		},
	})
}

// Name implements Channel.
func (c *BrevoClient) Name() string { return "brevo" }

// Send implements Channel: builds a plain-text RFC822 message and
// submits it to the Brevo relay via net/smtp, wrapped in the circuit
// breaker and a short backoff retry. Increments
// obs.AlertSendsTotal{brevo, ok|err}.
func (c *BrevoClient) Send(ctx context.Context, msg Message) error {
	addr := c.host + ":" + strconv.Itoa(c.port)
	auth := c.smtpAuth()
	body := c.buildMessage(msg)

	// The retry operation runs the breaker-gated send. A breaker that is
	// already open returns gobreaker.ErrOpenState immediately, so the
	// retry collapses fast during a sustained outage instead of burning
	// its whole budget.
	op := func() (struct{}, error) {
		_, err := c.cb.Execute(func() (struct{}, error) {
			if serr := c.sendMail(addr, auth, c.from, c.to, body); serr != nil {
				return struct{}{}, serr
			}
			return struct{}{}, nil
		})
		return struct{}{}, err
	}

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = brevoInitialBackoff

	_, err := backoff.Retry(ctx, op,
		backoff.WithBackOff(bo),
		backoff.WithMaxTries(brevoMaxRetries),
	)
	if err != nil {
		obs.AlertSendsTotal.WithLabelValues("brevo", "err").Inc()
		// err is a transport / auth / breaker-open error from net/smtp —
		// it does not embed the password. Wrapped with a fixed prefix so
		// even an unexpected smtp error string cannot leak a credential
		// via this layer (T-07-11).
		return fmt.Errorf("brevo: send failed: %w", err)
	}
	obs.AlertSendsTotal.WithLabelValues("brevo", "ok").Inc()
	return nil
}

// smtpAuth is the ONE place the SMTP credentials touch smtp.PlainAuth.
// Isolating it lets code review grep `smtp.PlainAuth` and confirm a
// single site that never flows the password into a log or error.
func (c *BrevoClient) smtpAuth() smtp.Auth {
	return smtp.PlainAuth("", c.user, c.pass, c.host)
}

// buildMessage renders a minimal plain-text RFC822 message: a To /
// From / Subject header block followed by the body. The Subject is the
// alert Title; the body is the alert Body.
func (c *BrevoClient) buildMessage(msg Message) []byte {
	var b strings.Builder
	b.WriteString("From: " + c.from + "\r\n")
	b.WriteString("To: " + strings.Join(c.to, ", ") + "\r\n")
	b.WriteString("Subject: " + msg.Title + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(msg.Body)
	b.WriteString("\r\n")
	return []byte(b.String())
}

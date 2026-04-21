// Package admin provides the X-Admin-Key bcrypt-backed admin auth middleware
// and the /admin/usage handler (D-D2, D-D3). This file declares the sentinel
// errors used by the middleware in Wave 4.
package admin

import "errors"

var (
	// ErrMissingAdminKey — request lacks X-Admin-Key header. HTTP 401.
	ErrMissingAdminKey = errors.New("admin: missing X-Admin-Key")
	// ErrInvalidAdminKey — bcrypt verify failed OR row.status='revoked'. HTTP 401.
	ErrInvalidAdminKey = errors.New("admin: invalid X-Admin-Key")
)

// Package main (admin_key.go): `gatewayctl admin-key` subcommand family
// used to provision / revoke / inspect the X-Admin-Key bcrypt-hashed
// credentials consumed by gateway/internal/admin/middleware.go.
//
//	create — generate an `ifix_admin_<hex>` raw key, bcrypt-hash it, INSERT
//	         into ai_gateway.admin_keys, and print the raw key ONCE to stdout.
//	         The slog logger NEVER receives the raw key; only the id + prefix
//	         + label are logged so operational logs are safe to ship.
//	revoke — marks the row status='revoked' + revoked_at=now(). Accepts
//	         --id UUID or --label LABEL. Label matches ALL active rows with
//	         that exact label.
//	list   — tabwriter dump of id, prefix, label, status, created, last_used.
//
// The hashing shape mirrors gateway/internal/admin/middleware.go: bcrypt
// cost 10 on the raw key (admin path low-frequency; ~50ms verify is
// acceptable) + SHA-256 lookup hash for PK-style fetch on the hot path.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// bcryptCost mirrors the cost used by the admin Middleware verifier. Do not
// change without coordinating with gateway/internal/admin/middleware.go —
// existing rows use this cost and re-verification will remain valid because
// bcrypt embeds cost in the hash.
const bcryptCost = 10

// adminKeyPrefixLen is how many raw-key hex characters are visible in the
// key_prefix column used for admin dashboards ("ifix_admin_****abcd").
const adminKeyPrefixLen = 4

// runAdminKey dispatches `gatewayctl admin-key <subcommand>`.
func runAdminKey(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: gatewayctl admin-key <create|revoke|list> [flags]")
		return 2
	}
	switch args[0] {
	case "create":
		return runAdminKeyCreate(ctx, args[1:], log)
	case "revoke":
		return runAdminKeyRevoke(ctx, args[1:], log)
	case "list":
		return runAdminKeyList(ctx, args[1:], log)
	default:
		fmt.Fprintf(os.Stderr, "unknown admin-key subcommand: %s\n", args[0])
		return 2
	}
}

// runAdminKeyCreate implements `gatewayctl admin-key create --label <label>`.
//
// The raw key is printed to stdout ONCE. Operators must copy it at that
// moment — nothing else in the system retains plaintext. The slog logger
// records only id/prefix/label (the httpx redactor will NOT catch a raw key
// accidentally slogged because the "key" attribute name is generic).
func runAdminKeyCreate(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("admin-key create", flag.ExitOnError)
	label := fs.String("label", "", "human label for this admin key (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *label == "" {
		fs.Usage()
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)

	// 1) Generate 16 random bytes → 32 hex chars → `ifix_admin_<hex>` (43 chars).
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		fmt.Fprintf(os.Stderr, "random: %v\n", err)
		return 1
	}
	raw := "ifix_admin_" + hex.EncodeToString(buf)

	// 2) SHA-256 lookup hash — enables PK-style fetch without scanning rows.
	sum := sha256.Sum256([]byte(raw))

	// 3) bcrypt hash — constant-time verify at middleware; stored at rest.
	bcryptHash, err := bcrypt.GenerateFromPassword([]byte(raw), bcryptCost)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bcrypt: %v\n", err)
		return 1
	}

	// 4) Display prefix: "ifix_admin_****" + last 4 hex chars. Mirrors the
	// api_keys.key_prefix pattern so both tables render uniformly in admin
	// dashboards.
	prefix := "ifix_admin_****" + raw[len(raw)-adminKeyPrefixLen:]

	ins, err := q.InsertAdminKey(ctx, gen.InsertAdminKeyParams{
		KeyLookupHash: sum[:],
		KeyHash:       string(bcryptHash),
		KeyPrefix:     prefix,
		Label:         *label,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "insert admin key: %v\n", err)
		return 1
	}

	// IMPORTANT: print the raw key to stdout ONCE. Do NOT log the raw key.
	// This mirrors the api_keys pattern in key.go lines 86-95.
	fmt.Printf("key=%s\nid=%s\nprefix=%s\nlabel=%s\n",
		raw, ins.ID.String(), prefix, *label)
	log.Info("admin key issued",
		"admin_key_id", ins.ID.String(),
		"label", *label,
		"key_prefix", prefix,
	)
	return 0
}

// runAdminKeyRevoke implements
//
//	gatewayctl admin-key revoke --id <uuid>
//	gatewayctl admin-key revoke --label <label>
//
// Either flag may be given (but not both). Label path revokes all currently
// active rows matching the label exactly — useful for rotating a label like
// "bootstrap" that may have been issued by migration.
func runAdminKeyRevoke(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("admin-key revoke", flag.ExitOnError)
	idStr := fs.String("id", "", "admin key UUID")
	label := fs.String("label", "", "revoke ALL active rows with this label")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *idStr == "" && *label == "" {
		fmt.Fprintln(os.Stderr, "either --id or --label is required")
		return 2
	}
	if *idStr != "" && *label != "" {
		fmt.Fprintln(os.Stderr, "pass only one of --id / --label")
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)

	// --id path: single UPDATE; rely on the ListAdminKeys query if we want
	// a post-verification check. pgx returns no rows-affected count so
	// typo'd ids look like no-ops from the caller's perspective.
	if *idStr != "" {
		id, err := uuid.Parse(*idStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid --id: %v\n", err)
			return 2
		}
		// Pre-verify the row exists so we can emit a clean "not found" error
		// and avoid the silent-no-op issue above.
		rows, listErr := q.ListAdminKeys(ctx)
		if listErr != nil {
			fmt.Fprintf(os.Stderr, "list admin keys: %v\n", listErr)
			return 1
		}
		found := false
		for _, r := range rows {
			if r.ID == id {
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "admin key %q not found\n", id.String())
			return 1
		}
		if err := q.RevokeAdminKey(ctx, id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				fmt.Fprintln(os.Stderr, "no such admin key id")
				return 1
			}
			fmt.Fprintf(os.Stderr, "revoke: %v\n", err)
			return 1
		}
		log.Info("admin key revoked", "admin_key_id", id.String())
		fmt.Printf("revoked: id=%s\n", id.String())
		return 0
	}

	// --label path: list → revoke all active matches.
	rows, err := q.ListAdminKeys(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list admin keys: %v\n", err)
		return 1
	}
	revoked := 0
	for _, r := range rows {
		if r.Status == "active" && r.Label == *label {
			if err := q.RevokeAdminKey(ctx, r.ID); err != nil {
				fmt.Fprintf(os.Stderr, "revoke %s: %v\n", r.ID.String(), err)
				return 1
			}
			log.Info("admin key revoked",
				"admin_key_id", r.ID.String(),
				"label", *label,
				"key_prefix", r.KeyPrefix,
			)
			revoked++
		}
	}
	if revoked == 0 {
		fmt.Fprintf(os.Stderr, "no active admin keys with label %q\n", *label)
		return 1
	}
	fmt.Printf("revoked: label=%s count=%d\n", *label, revoked)
	return 0
}

// runAdminKeyList dumps all admin_keys rows with key_prefix, label, status,
// created_at, last_used_at. Raw keys and bcrypt hashes are NEVER shown
// (neither the plaintext nor the hash is useful to operators — status/prefix
// is enough for auditing).
func runAdminKeyList(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("admin-key list", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)

	rows, err := q.ListAdminKeys(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list admin keys: %v\n", err)
		return 1
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPREFIX\tLABEL\tSTATUS\tCREATED\tLAST_USED")
	for _, r := range rows {
		lu := "-"
		if r.LastUsedAt.Valid {
			lu = r.LastUsedAt.Time.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID.String(), r.KeyPrefix, r.Label, r.Status,
			r.CreatedAt.UTC().Format(time.RFC3339), lu)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "flush: %v\n", err)
		return 1
	}
	return 0
}

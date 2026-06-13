# Phase 11 Plan 11-02 — UI Primitive Inventory

**Performed:** 2026-05-27
**Trigger:** Task 11-02-04 Step 0 [reviews MEDIUM #4] — BLOCKING gate before any install
**Inputs read:**
- `dashboard/src/components/ui/` (current shadcn primitives)
- `dashboard/package.json` (dependency baseline)
- `11-UI-SPEC.md` §Component Inventory + §Layout Constraints

## Existing primitives in `dashboard/src/components/ui/`

```
alert.tsx        button.tsx        calendar.tsx      card.tsx
chart.tsx        input.tsx         popover.tsx       scroll-area.tsx
select.tsx       separator.tsx     sheet.tsx         sidebar.tsx
skeleton.tsx     sonner.tsx        table.tsx         tabs.tsx
tooltip.tsx      badge.tsx
```

## Decisions per candidate net-new package

### input-otp

**Decision:** KEEP_EXISTING

**Rationale:** UI-SPEC v2 §Component Inventory line 226 says
"INSTALL only if existing `input` primitive cannot satisfy 6-slot
paste-handling + per-digit a11y AFTER inventory" and explicitly notes
"The prototype uses custom `.otp .slot` divs over plain `<input>` — that
pattern works without `input-otp`." We have a plain `Input` primitive
plus the prototype's `.otp .slot` token contract (44×52 slots, 8px gap,
blinking caret). The 6-slot OTP grid + paste-handling + per-digit a11y
is implementable in `~120 LOC` of pure React + Tailwind without a new
dep. KEEP_EXISTING — `OtpRow` (Task 4 Step 4) is hand-rolled on top of
the existing `Input` primitive using the prototype token pattern.

### @radix-ui/react-dialog (shadcn `dialog` block)

**Decision:** KEEP_EXISTING (no install)

**Rationale:** UI-SPEC v2 §Component Inventory line 227-228:
"Backup codes — prototype renders INLINE in LoginCard (no modal).
**Prefer inline** (no new dep)." The backup-codes step of TOTP enroll
renders the 10 codes inline in the same Card as the enroll flow (step 3
of the 3-step state machine). No modal needed.

### qrcode

**Decision:** INSTALL_NEW

**Rationale:** Required for TOTP enroll step 1 — the 192×192 QR code
that the operator scans with Authenticator/1Password. The plugin returns
`totpURI` (e.g. `otpauth://totp/Ifix%20AI%20Gateway:user@...?secret=...`)
and the UI renders it as a PNG via `QRCode.toDataURL(totpURI)`. No
existing dashboard dep provides QR rendering (verified via `grep` of
`package.json` — no `react-qr-code`, no `qr-code-styling`). Slopcheck
audit follows in Step 1.

### @types/qrcode

**Decision:** INSTALL_NEW (devDependency)

**Rationale:** TypeScript typings for `qrcode`. Tied directly to the
qrcode install decision above. The `qrcode` package ships JS only; the
`@types/qrcode` package on DefinitelyTyped provides the
`toDataURL(text, opts?)` signature used in enroll step 1.

### @playwright/test

**Decision:** INSTALL_NEW (devDependency)

**Rationale:** Task 11-02-05A ships a Playwright route-test gate that
asserts middleware redirect behavior end-to-end (4 cases). No Playwright
in baseline `package.json`. Slopcheck audit folded into Step 1 of Task 4.

## Summary

| Package           | Decision      | Reason                                                  |
|-------------------|---------------|---------------------------------------------------------|
| input-otp         | KEEP_EXISTING | UI-SPEC v2 default; hand-roll 6-slot on existing Input  |
| @radix-ui dialog  | KEEP_EXISTING | UI-SPEC v2 default; backup codes inline in same Card    |
| qrcode            | INSTALL_NEW   | Required for TOTP QR PNG render in enroll step 1        |
| @types/qrcode     | INSTALL_NEW   | TS typings for `qrcode`                                 |
| @playwright/test  | INSTALL_NEW   | Required for Task 11-02-05A route-test gate             |

**Net-new packages to install:** `qrcode`, `@types/qrcode`,
`@playwright/test`. Slopcheck audit on all three in Step 1 below.

**Net-new shadcn blocks to install:** none (input-otp + dialog rejected
by inventory).

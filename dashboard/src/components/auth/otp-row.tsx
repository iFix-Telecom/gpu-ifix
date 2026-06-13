"use client";

/**
 * OtpRow — 6-slot grouped-digit OTP input with paste-handling + per-digit
 * accessibility.
 *
 * Implementation rationale (Task 11-02-04 Step 0 inventory): chose
 * KEEP_EXISTING (no `input-otp` install) — pattern hand-rolled on the
 * prototype's `.otp .slot` token contract (UI-SPEC §Layout Constraints
 * line 145: 44×52 slots, 8px gap, blinking caret).
 *
 * Behavior:
 *   - 6 individual <input> slots, each accepts a single digit.
 *   - Typing in slot N auto-advances focus to slot N+1.
 *   - Backspace on an empty slot retreats focus to slot N-1 (and clears).
 *   - Pasting 6 digits anywhere fills all slots and focuses the last one.
 *   - ArrowLeft / ArrowRight navigate between slots.
 *   - `state` toggles the slot border colour for invalid / success cases
 *     per UI-SPEC §Color §badge variants.
 *
 * Tabular-numerals + monospace mandatory per UI-SPEC §Typography.
 */
import { type ReactElement, useCallback, useEffect, useId, useRef } from "react";

export interface OtpRowProps {
  /** Current 6-char string (caller-owned). */
  value: string;
  /** Called with the new 6-char string whenever any slot changes. */
  onChange: (v: string) => void;
  /** Visual state — default / invalid (red border) / success (primary border + tint). */
  state?: "default" | "invalid" | "success";
  /** When true, focus the first empty slot on mount. */
  autoFocus?: boolean;
  /** When true, all slots are disabled (e.g. during verify pending). */
  disabled?: boolean;
}

const SLOT_COUNT = 6;

function digitsOnly(s: string): string {
  return s.replace(/\D+/g, "");
}

function clampSix(s: string): string {
  return digitsOnly(s).slice(0, SLOT_COUNT);
}

export function OtpRow({
  value,
  onChange,
  state = "default",
  autoFocus = false,
  disabled = false,
}: OtpRowProps): ReactElement {
  const id = useId();
  const inputsRef = useRef<Array<HTMLInputElement | null>>([]);

  // Always work with a 6-char padded view internally so React sees a
  // controlled value for every slot.
  const padded = clampSix(value).padEnd(SLOT_COUNT, " ").slice(0, SLOT_COUNT);
  const chars = Array.from(padded).map((c) => (c === " " ? "" : c));

  useEffect(() => {
    if (autoFocus && !disabled) {
      const firstEmpty = chars.findIndex((c) => c === "");
      const target = firstEmpty === -1 ? 0 : firstEmpty;
      inputsRef.current[target]?.focus();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const focusAt = useCallback((idx: number) => {
    const i = Math.max(0, Math.min(SLOT_COUNT - 1, idx));
    inputsRef.current[i]?.focus();
    inputsRef.current[i]?.select();
  }, []);

  const setCharAt = useCallback(
    (idx: number, ch: string) => {
      const next = [...chars];
      next[idx] = ch;
      onChange(next.join(""));
    },
    [chars, onChange],
  );

  const handleChange = (idx: number) => (e: React.ChangeEvent<HTMLInputElement>) => {
    const raw = e.target.value;
    // Treat any paste/multi-char input here as fall-through to paste handler.
    if (raw.length > 1) {
      const trimmed = clampSix(raw);
      onChange(trimmed.padEnd(SLOT_COUNT, " ").slice(0, SLOT_COUNT).replaceAll(" ", ""));
      // Focus the last filled slot (or last slot if filled all 6).
      const lastFilled = Math.min(SLOT_COUNT - 1, trimmed.length - 1);
      focusAt(Math.max(0, lastFilled));
      return;
    }
    const ch = digitsOnly(raw).slice(-1); // last digit typed wins
    setCharAt(idx, ch);
    if (ch.length === 1 && idx < SLOT_COUNT - 1) focusAt(idx + 1);
  };

  const handleKeyDown =
    (idx: number) => (e: React.KeyboardEvent<HTMLInputElement>) => {
      if (e.key === "Backspace") {
        if (chars[idx] === "" && idx > 0) {
          e.preventDefault();
          setCharAt(idx - 1, "");
          focusAt(idx - 1);
        }
      } else if (e.key === "ArrowLeft" && idx > 0) {
        e.preventDefault();
        focusAt(idx - 1);
      } else if (e.key === "ArrowRight" && idx < SLOT_COUNT - 1) {
        e.preventDefault();
        focusAt(idx + 1);
      }
    };

  const handlePaste = (e: React.ClipboardEvent<HTMLInputElement>) => {
    e.preventDefault();
    const pasted = clampSix(e.clipboardData.getData("text"));
    if (!pasted) return;
    onChange(pasted);
    const last = Math.min(SLOT_COUNT - 1, pasted.length - 1);
    focusAt(Math.max(0, last));
  };

  // Slot styles per UI-SPEC §Color §badge variants:
  //   default — surface-tint fill, border-strong border
  //   invalid — destructive border + ring
  //   success — primary border + color-mix(--primary 10% --card) fill
  const slotBaseClass =
    "h-13 w-11 rounded-md border text-center text-2xl font-semibold leading-none tabular-nums font-mono outline-none transition-colors";
  const slotStateClass = (() => {
    if (state === "invalid") {
      return "border-destructive bg-[color-mix(in_oklch,var(--destructive)_8%,var(--card))] focus:ring-2 focus:ring-destructive/40";
    }
    if (state === "success") {
      return "border-[color:var(--primary)] bg-[color-mix(in_oklch,var(--primary)_10%,var(--card))] focus:ring-2 focus:ring-primary/40";
    }
    return "border-input bg-[color-mix(in_oklch,white_4%,var(--card))] focus:border-[color:var(--primary)] focus:ring-2 focus:ring-primary/40";
  })();

  return (
    <div
      className="flex items-center justify-center gap-2"
      role="group"
      aria-label="Código de verificação de 6 dígitos"
    >
      {chars.map((c, idx) => (
        <input
          // biome-ignore lint/suspicious/noArrayIndexKey: fixed-size 6-slot
          key={idx}
          ref={(el) => {
            inputsRef.current[idx] = el;
          }}
          id={`${id}-${idx}`}
          aria-label={`Dígito ${idx + 1} de 6`}
          inputMode="numeric"
          autoComplete={idx === 0 ? "one-time-code" : "off"}
          maxLength={1}
          disabled={disabled}
          value={c}
          onChange={handleChange(idx)}
          onKeyDown={handleKeyDown(idx)}
          onPaste={handlePaste}
          onFocus={(e) => e.currentTarget.select()}
          className={`${slotBaseClass} ${slotStateClass}`}
          style={{ width: "44px", height: "52px" }}
        />
      ))}
    </div>
  );
}

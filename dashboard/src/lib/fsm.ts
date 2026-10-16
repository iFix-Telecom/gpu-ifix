/**
 * Failover FSM state metadata — the single source of truth for the pt-BR
 * labels and the 3-tier status palette mapping from the UI-SPEC.
 *
 * UI-SPEC §Copywriting (FSM state labels) + §Semantic status palette:
 *   HEALTHY                → Saudável            → healthy  (--primary green)
 *   DEGRADED               → Degradado           → warning  (--status-warning)
 *   FAILED_OVER            → Em failover          → critical (--destructive)
 *   EMERGENCY_PROVISIONING → Provisionando pod    → warning  (--status-warning)
 *   EMERGENCY_ACTIVE       → Pod emergencial ativo→ critical (--destructive)
 *   RECOVERING             → Recuperando          → warning  (--status-warning)
 *   COOLDOWN               → Cooldown             → warning  (--status-warning)
 *   OFF_HOURS              → Fora de horário      → neutral  (--muted-foreground)
 *   MAINTENANCE            → Manutenção           → neutral  (--muted-foreground)
 */

/** The 3-tier status palette key (UI-SPEC §Semantic status palette). */
export type StatusTier = "healthy" | "warning" | "critical" | "neutral";

export interface FsmStateMeta {
  /** pt-BR operator-facing label. */
  label: string;
  /** Status palette tier driving the badge/indicator color. */
  tier: StatusTier;
}

/** Every FSM state the gateway's failover machine can report. */
export const FSM_STATE_META: Record<string, FsmStateMeta> = {
  HEALTHY: { label: "Saudável", tier: "healthy" },
  DEGRADED: { label: "Degradado", tier: "warning" },
  FAILED_OVER: { label: "Em failover", tier: "critical" },
  EMERGENCY_PROVISIONING: { label: "Provisionando pod", tier: "warning" },
  EMERGENCY_ACTIVE: { label: "Pod emergencial ativo", tier: "critical" },
  RECOVERING: { label: "Recuperando", tier: "warning" },
  COOLDOWN: { label: "Cooldown", tier: "warning" },
  OFF_HOURS: { label: "Fora de horário", tier: "neutral" },
  MAINTENANCE: { label: "Manutenção", tier: "neutral" },
};

/** FSM states that raise the sticky RED critical banner. */
export const CRITICAL_FSM_STATES = ["FAILED_OVER", "EMERGENCY_ACTIVE"] as const;

/** FSM states that raise the AMBER warning banner. */
export const WARNING_FSM_STATES = [
  "DEGRADED",
  "EMERGENCY_PROVISIONING",
  "RECOVERING",
  "COOLDOWN",
] as const;

/**
 * The gateway reports the canonical LOWERCASE name (`emerg.State.String()`:
 * "healthy", "failed_over"…) while the tables above are keyed UPPERCASE.
 * Without this every lookup missed: raw label, neutral tier, and the critical
 * banner never fired (e.g. the 14–15/10/2026 failed_over loop).
 */
function normalizeState(state: string | undefined | null): string | null {
  return state ? state.toUpperCase() : null;
}

/** Resolve an FSM state string to its label + tier, with a neutral fallback. */
export function fsmMeta(state: string | undefined | null): FsmStateMeta {
  const key = normalizeState(state);
  if (key && key in FSM_STATE_META) {
    return FSM_STATE_META[key];
  }
  return { label: state ?? "Desconhecido", tier: "neutral" };
}

/** True when the state should raise the sticky red critical banner. */
export function isCriticalFsmState(state: string | undefined | null): boolean {
  return (
    !!state &&
    (CRITICAL_FSM_STATES as readonly string[]).includes(normalizeState(state)!)
  );
}

/** True when the state should raise the amber warning banner. */
export function isWarningFsmState(state: string | undefined | null): boolean {
  return (
    !!state &&
    (WARNING_FSM_STATES as readonly string[]).includes(normalizeState(state)!)
  );
}

/** Tailwind text-color class for a status tier (UI-SPEC palette). */
export function tierTextClass(tier: StatusTier): string {
  switch (tier) {
    case "healthy":
      return "text-primary";
    case "warning":
      return "text-status-warning";
    case "critical":
      return "text-destructive";
    case "neutral":
      return "text-muted-foreground";
  }
}

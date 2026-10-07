/**
 * Routing-order sort for model-alias rows (quick 261007-gyq).
 *
 * The gateway tries the upstreams of a pinned alias in `tier`, then
 * `tier_priority` order (e.g. whisper: local-stt 0/0 → gemini-stt 1/10 →
 * groq-whisper 1/15 → openai-whisper 2/20). The admin API already returns rows
 * in that order; this client-side sort is defensive for an older gateway that
 * still answers alphabetically. Rows whose upstream is unknown sort last within
 * their alias; ties break on upstream name.
 *
 * @module alias-order
 */
import type { ModelAliasRow, UpstreamRow } from "./gateway";

/** Tier info for one upstream, looked up by name. */
export interface UpstreamTier {
  tier: number;
  tierPriority: number;
}

/** Builds the name → {tier, tierPriority} lookup from the loaded upstreams. */
export function buildTierIndex(
  upstreams: Pick<UpstreamRow, "name" | "tier" | "tier_priority">[],
): Map<string, UpstreamTier> {
  const m = new Map<string, UpstreamTier>();
  for (const u of upstreams) {
    m.set(u.name, { tier: u.tier, tierPriority: u.tier_priority ?? 0 });
  }
  return m;
}

/**
 * Returns a NEW array sorted by (alias, tier, tier_priority, upstream_name),
 * unknown upstreams last within the alias — the gateway's attempt order.
 */
export function sortAliasRowsByRouting<T extends Pick<ModelAliasRow, "alias" | "upstream_name">>(
  rows: T[],
  tiers: Map<string, UpstreamTier>,
): T[] {
  const INF = Number.POSITIVE_INFINITY;
  return [...rows].sort((a, b) => {
    if (a.alias !== b.alias) return a.alias < b.alias ? -1 : 1;
    const ta = tiers.get(a.upstream_name);
    const tb = tiers.get(b.upstream_name);
    const d1 = (ta?.tier ?? INF) - (tb?.tier ?? INF);
    if (d1 !== 0 && !Number.isNaN(d1)) return d1;
    const d2 = (ta?.tierPriority ?? INF) - (tb?.tierPriority ?? INF);
    if (d2 !== 0 && !Number.isNaN(d2)) return d2;
    if (a.upstream_name === b.upstream_name) return 0;
    return a.upstream_name < b.upstream_name ? -1 : 1;
  });
}

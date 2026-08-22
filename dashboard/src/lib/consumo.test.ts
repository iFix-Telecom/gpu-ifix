import { describe, expect, it } from "vitest";

import { fillDateGaps, topTenantsByVolume } from "@/lib/consumo";
import type { UsageResponse } from "@/lib/gateway";

/**
 * These two helpers carry the honesty rules of the Consumo screen:
 *   - a tenant's modality is DERIVED from its counters, never guessed;
 *   - a day with no billing row renders as a GAP (null), never as zero.
 */

type Counters = Partial<UsageResponse["summary"]>;

function usage(
  slug: string,
  requests_count: number,
  counters: Counters = {},
): UsageResponse {
  return {
    tenant: {
      id: `id-${slug}`,
      slug,
      name: slug,
      data_class: "normal",
      mode: "auto",
    },
    range: {
      from: "2026-08-01",
      to: "2026-08-05",
      granularity: "day",
      timezone: "America/Sao_Paulo",
    },
    summary: {
      tokens_in: 0,
      tokens_out: 0,
      audio_seconds: 0,
      embeds_count: 0,
      cost_local_brl: 0,
      cost_local_phantom_brl: 0,
      cost_external_brl: 0,
      cost_total_brl: 0,
      requests_count,
      ...counters,
    },
    rows: [],
  };
}

describe("topTenantsByVolume", () => {
  it("sorts by requests_count descending and honours the limit", () => {
    const rows = topTenantsByVolume(
      [
        usage("small", 10, { tokens_in: 1 }),
        usage("huge", 5000, { tokens_in: 1 }),
        usage("mid", 300, { tokens_in: 1 }),
      ],
      2,
    );
    expect(rows.map((r) => r.label)).toEqual(["huge", "mid"]);
    expect(rows[0].requests_count).toBe(5000);
  });

  it("derives the modality from the usage counters", () => {
    const [stt, embed, llm, mixed] = topTenantsByVolume([
      usage("stt-only", 40, { audio_seconds: 900 }),
      usage("embed-only", 30, { embeds_count: 220 }),
      usage("llm-only", 20, { tokens_in: 1000, tokens_out: 400 }),
      // Audio AND tokens → the token spend dominates the reading, so "llm".
      usage("mixed", 10, { audio_seconds: 500, tokens_in: 90 }),
    ]);
    expect(stt.modality).toBe("stt");
    expect(embed.modality).toBe("embed");
    expect(llm.modality).toBe("llm");
    expect(mixed.modality).toBe("llm");
  });
});

describe("fillDateGaps", () => {
  it("emits null (not zero) for days with no billing row and keeps the rest", () => {
    const filled = fillDateGaps(
      [
        { date: "2026-08-01", tokens: 100, cost_brl: 1.5 },
        { date: "2026-08-04", tokens: 200, cost_brl: 2.5 },
      ],
      "2026-08-01",
      "2026-08-05",
    );

    expect(filled).toHaveLength(5);
    expect(filled.map((r) => r.date)).toEqual([
      "2026-08-01",
      "2026-08-02",
      "2026-08-03",
      "2026-08-04",
      "2026-08-05",
    ]);
    expect(filled.map((r) => r.tokens)).toEqual([100, null, null, 200, null]);
    expect(filled[1].cost_brl).toBeNull();
    // The critical assertion: a missing day must NOT read as a zero day.
    expect(filled[1].tokens).not.toBe(0);
    expect(filled[3].cost_brl).toBe(2.5);
  });

  it("covers a month boundary without skipping or duplicating a day", () => {
    const filled = fillDateGaps([], "2026-07-30", "2026-08-02");
    expect(filled.map((r) => r.date)).toEqual([
      "2026-07-30",
      "2026-07-31",
      "2026-08-01",
      "2026-08-02",
    ]);
  });
});

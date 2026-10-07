import { describe, expect, it } from "vitest";

import { buildTierIndex, sortAliasRowsByRouting } from "@/lib/alias-order";

// Real production cascade (2026-10-07): whisper routes local-stt → gemini-stt
// → groq-whisper → openai-whisper, which is NOT alphabetical.
const upstreams = [
  { name: "openai-whisper", tier: 2, tier_priority: 20 },
  { name: "groq-whisper", tier: 1, tier_priority: 15 },
  { name: "gemini-stt", tier: 1, tier_priority: 10 },
  { name: "local-stt", tier: 0, tier_priority: 0 },
  { name: "local-llm", tier: 0, tier_priority: 0 },
  { name: "openrouter-chat", tier: 1, tier_priority: 0 },
];

const row = (alias: string, upstream_name: string) => ({ alias, upstream_name });

describe("sortAliasRowsByRouting", () => {
  const tiers = buildTierIndex(upstreams);

  it("orders rows of one alias by tier then tier_priority (attempt order)", () => {
    const rows = [
      row("whisper", "gemini-stt"),
      row("whisper", "groq-whisper"),
      row("whisper", "local-stt"),
      row("whisper", "openai-whisper"),
    ];
    expect(sortAliasRowsByRouting(rows, tiers).map((r) => r.upstream_name)).toEqual([
      "local-stt",
      "gemini-stt",
      "groq-whisper",
      "openai-whisper",
    ]);
  });

  it("groups by alias first and puts unknown upstreams last within the alias", () => {
    const rows = [
      row("whisper", "zz-unknown"),
      row("whisper", "openai-whisper"),
      row("qwen", "openrouter-chat"),
      row("whisper", "local-stt"),
      row("qwen", "local-llm"),
      row("whisper", "aa-unknown"),
    ];
    expect(sortAliasRowsByRouting(rows, tiers).map((r) => `${r.alias}:${r.upstream_name}`)).toEqual([
      "qwen:local-llm",
      "qwen:openrouter-chat",
      "whisper:local-stt",
      "whisper:openai-whisper",
      "whisper:aa-unknown",
      "whisper:zz-unknown",
    ]);
  });

  it("does not mutate the input array", () => {
    const rows = [row("whisper", "openai-whisper"), row("whisper", "local-stt")];
    const copy = [...rows];
    sortAliasRowsByRouting(rows, tiers);
    expect(rows).toEqual(copy);
  });
});

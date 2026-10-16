import { describe, expect, it } from "vitest";

import { formatCompact, formatPercent } from "@/lib/format";

const nb = (s: string) => s.replace(/ /g, " ");

describe("formatPercent", () => {
  it("formats a 0–100 value with one pt-BR decimal", () => {
    // O budget vinha cru do gateway: 7.595969999999999994%.
    expect(nb(formatPercent(7.595969999999999994))).toBe("7,6 %");
    expect(nb(formatPercent(100))).toBe("100,0 %");
  });
});

describe("formatCompact", () => {
  it("abbreviates big counts so KPI tiles do not overflow", () => {
    expect(nb(formatCompact(413731623))).toBe("413,7 mi");
    expect(nb(formatCompact(27074))).toBe("27,1 mil");
    expect(nb(formatCompact(512))).toBe("512");
  });
});

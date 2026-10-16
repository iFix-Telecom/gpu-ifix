import { describe, expect, it } from "vitest";

import { formatTenant } from "@/components/audit-table";

describe("formatTenant", () => {
  it("shows 'sistema' for gateway-internal events (zero uuid)", () => {
    expect(formatTenant("00000000-0000-0000-0000-000000000000")).toBe("sistema");
    expect(formatTenant("")).toBe("sistema");
  });

  it("resolves a tenant id to its name, falling back to the raw id", () => {
    const names = { "d708": "Chat iFix" };
    expect(formatTenant("d708", names)).toBe("Chat iFix");
    expect(formatTenant("abcd", names)).toBe("abcd");
  });
});

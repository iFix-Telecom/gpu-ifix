import { describe, expect, it } from "vitest";
import { cn } from "@/lib/utils";

/**
 * Smoke test placeholder — confirms the vitest + jsdom + `@/` alias toolchain
 * is wired before 07-08 adds real component/wrapper tests.
 */
describe("dashboard toolchain smoke", () => {
  it("resolves the @/ alias and runs cn()", () => {
    expect(cn("a", false && "b", "c")).toBe("a c");
  });
});

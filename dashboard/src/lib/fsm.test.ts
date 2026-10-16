import { describe, expect, it } from "vitest";

import { fsmMeta, isCriticalFsmState, isWarningFsmState } from "@/lib/fsm";

/** Valores exatamente como o gateway emite (emerg.State.String(), minúsculas). */
describe("fsm — estados do gateway em minúsculas", () => {
  it("resolve label e tier", () => {
    expect(fsmMeta("healthy")).toEqual({ label: "Saudável", tier: "healthy" });
    expect(fsmMeta("failed_over").tier).toBe("critical");
    expect(fsmMeta("emergency_provisioning").label).toBe("Provisionando pod");
  });

  it("acende os banners", () => {
    expect(isCriticalFsmState("failed_over")).toBe(true);
    expect(isCriticalFsmState("emergency_active")).toBe(true);
    expect(isWarningFsmState("cooldown")).toBe(true);
    expect(isCriticalFsmState("healthy")).toBe(false);
  });

  it("estado desconhecido cai no neutro com o texto cru", () => {
    expect(fsmMeta("unknown")).toEqual({ label: "unknown", tier: "neutral" });
    expect(fsmMeta(undefined).label).toBe("Desconhecido");
  });
});

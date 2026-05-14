import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { FsmPanel } from "@/components/fsm-panel";
import { FSM_STATE_META } from "@/lib/fsm";

/**
 * The FSM panel maps each gateway `fsm_state` to its pt-BR label + the
 * 3-tier status-palette color (UI-SPEC §Copywriting + §Semantic status
 * palette). Every state in `FSM_STATE_META` must resolve to a label + tier,
 * and at minimum HEALTHY (green), FAILED_OVER (red), and a neutral state
 * (OFF_HOURS) must map correctly.
 */

describe("FsmPanel", () => {
  it("maps HEALTHY to 'Saudável' with the healthy (green) tier", () => {
    render(<FsmPanel fsmState="HEALTHY" />);
    const badge = screen.getByText("Saudável");
    expect(badge).toHaveAttribute("data-tier", "healthy");
    expect(badge.className).toContain("text-primary");
  });

  it("maps FAILED_OVER to 'Em failover' with the critical (red) tier", () => {
    render(<FsmPanel fsmState="FAILED_OVER" />);
    const badge = screen.getByText("Em failover");
    expect(badge).toHaveAttribute("data-tier", "critical");
    expect(badge.className).toContain("text-destructive");
  });

  it("maps OFF_HOURS to 'Fora de horário' with the neutral tier", () => {
    render(<FsmPanel fsmState="OFF_HOURS" />);
    const badge = screen.getByText("Fora de horário");
    expect(badge).toHaveAttribute("data-tier", "neutral");
    expect(badge.className).toContain("text-muted-foreground");
  });

  it("maps a warning state (DEGRADED) to the amber tier", () => {
    render(<FsmPanel fsmState="DEGRADED" />);
    const badge = screen.getByText("Degradado");
    expect(badge).toHaveAttribute("data-tier", "warning");
    expect(badge.className).toContain("text-status-warning");
  });

  it("renders a pt-BR label for every FSM state in the contract", () => {
    for (const [state, meta] of Object.entries(FSM_STATE_META)) {
      const { unmount } = render(<FsmPanel fsmState={state} />);
      expect(screen.getByText(meta.label)).toHaveAttribute(
        "data-tier",
        meta.tier,
      );
      unmount();
    }
  });
});

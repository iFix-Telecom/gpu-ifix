import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { EconomyPanel } from "@/components/economy-panel";
import type { EconomyResponse } from "@/lib/gateway";

/**
 * 23-13 (HAIKU-09): the Economia panel must separate the Haiku step ①
 * ("coberto por créditos", caixa real = 0) from real external cash spend,
 * show % local by TOKENS (requests as secondary) and break out the 3060.
 */

type Summary = EconomyResponse["summary"];

function makeSummary(over: Partial<Summary> = {}): Summary {
  return {
    phantom_brl: 120.5,
    vast_brl: 30,
    economia_liquida_brl: 90.5,
    roi_multiplier: 4.0166,
    custo_openrouter_brl: 12.34,
    pct_servido_local: 0.87,
    horas_pod_up: 56.5,
    pct_servido_local_tokens: 0.625,
    custo_creditos_haiku_brl: 10,
    custo_externo_caixa_brl: 2.34,
    vast_3060_brl: 4.5,
    ...over,
  };
}

/** Text of the KpiCard (its card-content) whose caption matches. */
function cardText(caption: string): string {
  const el = screen.getByText(caption);
  const container = el.closest("[data-slot='card-content']");
  if (!container) throw new Error(`card not found: ${caption}`);
  // Intl pt-BR currency uses U+00A0 between "R$" and the number.
  return (container.textContent ?? "").replace(/ /g, " ");
}

describe("EconomyPanel (23-13)", () => {
  it("shows Haiku cost as 'coberto por créditos' with real cash R$ 0,00", () => {
    render(<EconomyPanel summary={makeSummary()} />);
    const c = cardText("Haiku (coberto por créditos)");
    expect(c).toContain("R$ 10,00");
    expect(c).toContain("Caixa real: R$ 0,00");
  });

  it("'Custo externo (caixa)' uses custo_externo_caixa_brl, not the total", () => {
    render(<EconomyPanel summary={makeSummary()} />);
    const c = cardText("Custo externo (caixa)");
    expect(c).toContain("R$ 2,34");
    expect(c).not.toContain("12,34");
    expect(screen.queryByText("Custo OpenRouter")).toBeNull();
  });

  it("'% servido local' shows tokens as main value and requests in the hint", () => {
    render(<EconomyPanel summary={makeSummary()} />);
    const c = cardText("% servido local");
    expect(c).toContain("62,5 %");
    expect(c).toContain("87,0 %");
    expect(c.toLowerCase()).toContain("requests");
  });

  it("'% servido local' renders — when the tokens fraction is null", () => {
    render(
      <EconomyPanel
        summary={makeSummary({
          pct_servido_local_tokens: null,
          pct_servido_local: null,
        })}
      />,
    );
    const c = cardText("% servido local");
    expect(c).toContain("—");
    expect(c).not.toContain("NaN");
  });

  it("Vast card shows vast_brl total and the 3060 share in the hint", () => {
    render(<EconomyPanel summary={makeSummary()} />);
    const c = cardText("Custo Vast (GPU)");
    expect(c).toContain("R$ 30,00");
    expect(c).toContain("3060: R$ 4,50");
  });
});

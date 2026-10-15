import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { LocalVsExternalDonut } from "@/components/local-vs-external-donut";
import type { EconomyResponse } from "@/lib/gateway";

/**
 * Phase 23 (pendência do 23-13): o donut "Local vs externo" não pode somar o
 * Haiku do passo ① (coberto por créditos) como gasto "pago". Usa o % local
 * por TOKENS, igual ao painel Economia, e separa créditos de caixa.
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

const text = (c: HTMLElement) => (c.textContent ?? "").replace(/ /g, " ");

describe("LocalVsExternalDonut (Phase 23)", () => {
  it("shows % local by tokens, not by requests", () => {
    const { container } = render(<LocalVsExternalDonut summary={makeSummary()} />);
    expect(text(container)).toContain("62,5%");
    expect(text(container)).not.toContain("87,0%");
  });

  it("splits Haiku credits from real external cash", () => {
    const { container } = render(<LocalVsExternalDonut summary={makeSummary()} />);
    const t = text(container);
    expect(screen.getByText("Haiku (créditos)")).toBeTruthy();
    expect(t).toContain("R$ 10,00");
    expect(t).toContain("coberto por créditos");
    expect(screen.getByText("Externo (caixa)")).toBeTruthy();
    expect(t).toContain("R$ 2,34");
    // O total externo (Haiku + caixa) não aparece mais como "pagos".
    expect(t).not.toContain("12,34");
  });

  it("renders the empty state when tokens share is null", () => {
    render(
      <LocalVsExternalDonut summary={makeSummary({ pct_servido_local_tokens: null })} />,
    );
    expect(screen.getByText("Sem dados no período.")).toBeTruthy();
  });
});

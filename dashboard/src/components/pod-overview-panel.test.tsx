import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { PodOverviewPanel } from "@/components/pod-overview-panel";
import type {
  MetricsResponse,
  OperationsResponse,
  OperationsSecondaryPod,
} from "@/lib/gateway";

/**
 * "Por pod" must show the FSM-managed primary AND every secondary Vast
 * instance — the whole point of the section is that the 3060 stopped being
 * invisible on the overview.
 */

function makeOperations(
  secondary_pods: OperationsSecondaryPod[],
  primary_state = "ready",
): OperationsResponse {
  return {
    fsm: {
      primary_state,
      emerg_state: "unknown",
      active_lifecycle_id: 42,
      active_instance_id: 99887766,
      is_leader: true,
    },
    schedule: {
      timezone: "America/Sao_Paulo",
      up_hour: 9,
      down_hour: 17,
      days: ["mon"],
      provision_lead_seconds: 0,
      grace_ramp_down_seconds: 0,
      disabled: false,
      should_be_provisioned_now: true,
      next_transition_at: "",
      next_transition_kind: "",
    },
    lifecycles: [],
    breakers: [{ upstream: "local-llm", state: "closed" }],
    vast_cost: {
      today_brl: 6.42,
      month_brl: 84.71,
      budget_brl: 1000,
      budget_pct_used: 8.5,
    },
    secondary_pods,
  };
}

function makePod(id: number): OperationsSecondaryPod {
  return {
    id,
    gpu_name: "RTX 3060",
    num_gpus: 1,
    status: "running",
    label: `pod-${id}`,
    dph_brl: 0.13,
    uptime_seconds: 50400,
  };
}

const metrics: MetricsResponse = {
  window: "5m",
  fsm_state: "HEALTHY",
  tenants: [
    {
      tenant_id: "t1",
      tenant_slug: "chat-ifix",
      tenant_name: "Chat iFix",
      route: "/v1/chat/completions",
      p50: 900,
      p95: 3200,
      p99: 3300,
      requests: 120,
      error_rate: 0,
    },
  ],
  inflight: [],
};

describe("PodOverviewPanel", () => {
  it("renders the primary pod's FSM state label", () => {
    render(
      <PodOverviewPanel
        operations={makeOperations([makePod(1)], "asleep")}
        metrics={metrics}
      />,
    );
    expect(screen.getByText("Pod primário — LLM")).toBeInTheDocument();
    expect(screen.getByText("dormindo")).toBeInTheDocument();
  });

  it("renders one card per secondary pod", () => {
    render(
      <PodOverviewPanel
        operations={makeOperations([makePod(48294952), makePod(51110001)])}
        metrics={metrics}
      />,
    );
    expect(screen.getAllByText("Pod secundário")).toHaveLength(2);
    expect(screen.getByText("#48294952")).toBeInTheDocument();
    expect(screen.getByText("#51110001")).toBeInTheDocument();
  });

  it("shows the empty state when there is no secondary pod", () => {
    render(
      <PodOverviewPanel operations={makeOperations([])} metrics={metrics} />,
    );
    expect(screen.getByText("Nenhum outro pod ativo.")).toBeInTheDocument();
    expect(screen.queryByText("Pod secundário")).toBeNull();
  });
});

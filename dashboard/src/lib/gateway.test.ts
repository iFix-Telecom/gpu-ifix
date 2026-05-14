import { afterEach, describe, expect, it, vi } from "vitest";
import {
  GatewayError,
  fetchAudit,
  fetchMetrics,
  fetchUsage,
} from "@/lib/gateway";

/**
 * gateway.ts wrappers must (a) always hit the SERVER-SIDE proxy at
 * `/api/gateway/*` — never GATEWAY_BASE_URL directly — and (b) parse the
 * gateway's 07-03 / Phase-4 JSON shapes. `fetch` is mocked so no network or
 * admin key is involved.
 */

function mockFetchOnce(body: unknown, init?: { ok?: boolean; status?: number }) {
  const ok = init?.ok ?? true;
  const status = init?.status ?? 200;
  return vi.fn().mockResolvedValueOnce({
    ok,
    status,
    json: async () => body,
  } as Response);
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("fetchMetrics", () => {
  it("hits the /api/gateway/metrics proxy path and parses MetricsResponse", async () => {
    const payload = {
      window: "5m",
      generated_at: "2026-05-14T09:00:00Z",
      tenants: [
        {
          tenant_id: "converseai",
          route: "/v1/chat/completions",
          p50: 120,
          p95: 480,
          p99: 900,
          requests: 42,
          error_rate: 0.0,
        },
      ],
      by_route: [{ key: "/v1/chat/completions", p50: 120, p95: 480, p99: 900 }],
      by_upstream: [{ key: "local", p50: 100, p95: 400, p99: 800 }],
      inflight: 3,
      fsm_state: "HEALTHY",
    };
    const fetchMock = mockFetchOnce(payload);
    vi.stubGlobal("fetch", fetchMock);

    const result = await fetchMetrics("5m");

    const calledUrl = fetchMock.mock.calls[0][0] as string;
    expect(calledUrl).toBe("/api/gateway/metrics?window=5m");
    // Never the gateway directly.
    expect(calledUrl.startsWith("/api/gateway/")).toBe(true);
    expect(result.fsm_state).toBe("HEALTHY");
    expect(result.tenants[0].p95).toBe(480);
  });
});

describe("fetchAudit", () => {
  it("hits /api/gateway/audit with limit + offset and parses AuditResponse", async () => {
    const payload = {
      rows: [
        {
          id: "evt-1",
          ts: "2026-05-14T08:59:00Z",
          event_kind: "fsm_transition",
          tenant_id: null,
          actor: "system",
          detail: { from: "HEALTHY", to: "DEGRADED" },
        },
      ],
      limit: 25,
      offset: 50,
      total: 1,
    };
    const fetchMock = mockFetchOnce(payload);
    vi.stubGlobal("fetch", fetchMock);

    const result = await fetchAudit(25, 50);

    const calledUrl = fetchMock.mock.calls[0][0] as string;
    expect(calledUrl).toBe("/api/gateway/audit?limit=25&offset=50");
    expect(result.rows[0].event_kind).toBe("fsm_transition");
    expect(result.total).toBe(1);
  });
});

describe("fetchUsage", () => {
  it("hits /api/gateway/usage with tenant + from + to query params", async () => {
    const payload = {
      tenant: {
        id: "uuid",
        slug: "converseai",
        name: "ConverseAI",
        data_class: "normal",
        mode: "24/7",
      },
      range: {
        from: "2026-05-01",
        to: "2026-05-14",
        granularity: "day",
        timezone: "America/Sao_Paulo",
      },
      summary: {
        tokens_in: 1000,
        tokens_out: 2000,
        audio_seconds: 0,
        embeds_count: 0,
        cost_local_brl: 1.5,
        cost_local_phantom_brl: 0,
        cost_external_brl: 0.5,
        cost_total_brl: 2.0,
        requests_count: 10,
      },
      rows: [],
    };
    const fetchMock = mockFetchOnce(payload);
    vi.stubGlobal("fetch", fetchMock);

    const result = await fetchUsage("converseai", "2026-05-01", "2026-05-14");

    const calledUrl = fetchMock.mock.calls[0][0] as string;
    expect(calledUrl.startsWith("/api/gateway/usage?")).toBe(true);
    expect(calledUrl).toContain("tenant=converseai");
    expect(calledUrl).toContain("from=2026-05-01");
    expect(calledUrl).toContain("to=2026-05-14");
    expect(result.summary.cost_total_brl).toBe(2.0);
  });
});

describe("error handling", () => {
  it("throws GatewayError on a non-2xx proxy response", async () => {
    const fetchMock = mockFetchOnce(
      { error: { message: "boom" } },
      { ok: false, status: 502 },
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(fetchMetrics()).rejects.toBeInstanceOf(GatewayError);
  });
});

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  type AuditResponse,
  type EconomyResponse,
  GatewayError,
  fetchAudit,
  fetchEconomy,
  fetchMetrics,
  fetchUsage,
  tenantLabel,
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
    // This payload is the ACTUAL `admin.MetricsResponse` shape the Go
    // handler emits (gateway/internal/admin/metrics.go) — window,
    // fsm_state, tenants[] with raw-UUID tenant_id plus the WR-10
    // tenant_slug / tenant_name from the tenants LEFT JOIN, inflight as
    // an InflightRow[] array. No generated_at / by_route / by_upstream /
    // scalar inflight — the gateway never emits those.
    const payload = {
      window: "5m0s",
      fsm_state: "healthy",
      tenants: [
        {
          tenant_id: "8f1c0d2e-4a5b-6c7d-8e9f-0a1b2c3d4e5f",
          tenant_slug: "converseai",
          tenant_name: "ConverseAI",
          route: "/v1/chat/completions",
          p50: 120,
          p95: 480,
          p99: 900,
          requests: 42,
          error_rate: 0.0,
        },
      ],
      inflight: [
        { upstream: "local-llm", inflight: 3 },
        { upstream: "openrouter-chat", inflight: 0 },
      ],
    };
    const fetchMock = mockFetchOnce(payload);
    vi.stubGlobal("fetch", fetchMock);

    const result = await fetchMetrics("5m");

    const calledUrl = fetchMock.mock.calls[0][0] as string;
    expect(calledUrl).toBe("/api/gateway/metrics?window=5m");
    // Never the gateway directly.
    expect(calledUrl.startsWith("/api/gateway/")).toBe(true);
    expect(result.fsm_state).toBe("healthy");
    expect(result.tenants[0].p95).toBe(480);
    expect(result.tenants[0].tenant_slug).toBe("converseai");
    expect(result.tenants[0].tenant_name).toBe("ConverseAI");
    expect(result.inflight[0].upstream).toBe("local-llm");
    expect(result.inflight[0].inflight).toBe(3);
  });
});

describe("tenantLabel", () => {
  it("prefers name, falls back to slug, then the raw UUID (WR-10)", () => {
    expect(
      tenantLabel({
        tenant_id: "8f1c0d2e-4a5b-6c7d-8e9f-0a1b2c3d4e5f",
        tenant_slug: "converseai",
        tenant_name: "ConverseAI",
      }),
    ).toBe("ConverseAI");
    expect(
      tenantLabel({
        tenant_id: "8f1c0d2e-4a5b-6c7d-8e9f-0a1b2c3d4e5f",
        tenant_slug: "converseai",
        tenant_name: null,
      }),
    ).toBe("converseai");
    expect(
      tenantLabel({
        tenant_id: "8f1c0d2e-4a5b-6c7d-8e9f-0a1b2c3d4e5f",
        tenant_slug: null,
        tenant_name: null,
      }),
    ).toBe("8f1c0d2e-4a5b-6c7d-8e9f-0a1b2c3d4e5f");
  });
});

describe("fetchAudit", () => {
  it("hits /api/gateway/audit with limit + offset and parses AuditResponse", async () => {
    // This payload is the ACTUAL `admin.AuditResponse` shape the Go
    // handler emits (gateway/internal/admin/audit.go) — `items` (not
    // `rows`), a real `total` COUNT (15-02), and AuditRow carries the
    // request-metadata columns (request_id, route, method, status_code,
    // latency_ms, error_code, event_kind, reason) — no id / actor / detail.
    const payload: AuditResponse = {
      items: [
        {
          ts: "2026-05-14T08:59:00Z",
          request_id: "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d",
          tenant_id: "00000000-0000-0000-0000-000000000000",
          route: "emerg_fsm_transition",
          method: "healthy->degraded",
          upstream: "degraded",
          status_code: 0,
          latency_ms: 0,
          error_code: null,
          event_kind: "fsm_transition",
          reason: "breaker_flap",
        },
      ],
      limit: 25,
      offset: 50,
      total: 137,
    };
    const fetchMock = mockFetchOnce(payload);
    vi.stubGlobal("fetch", fetchMock);

    const result = await fetchAudit(25, 50);

    const calledUrl = fetchMock.mock.calls[0][0] as string;
    expect(calledUrl).toBe("/api/gateway/audit?limit=25&offset=50");
    expect(result.items[0].event_kind).toBe("fsm_transition");
    expect(result.items[0].reason).toBe("breaker_flap");
    expect(result.limit).toBe(25);
    expect(result.offset).toBe(50);
    // 15-02 added a real COUNT(*) so the pager can derive honest bounds.
    expect(result.total).toBe(137);
  });

  it("forwards from/to/search query params when provided", async () => {
    // OBS-10: /incidents gains a date-range + free-text filter. The
    // optional from/to/search must travel to the gateway as query params
    // (the parameterized ILIKE / BRT range lives in the Go handler — 15-02).
    const payload: AuditResponse = { items: [], limit: 50, offset: 0, total: 0 };
    const fetchMock = mockFetchOnce(payload);
    vi.stubGlobal("fetch", fetchMock);

    await fetchAudit(50, 0, "2026-06-01", "2026-06-30", "503");

    const calledUrl = fetchMock.mock.calls[0][0] as string;
    expect(calledUrl.startsWith("/api/gateway/audit?")).toBe(true);
    expect(calledUrl).toContain("limit=50");
    expect(calledUrl).toContain("offset=0");
    expect(calledUrl).toContain("from=2026-06-01");
    expect(calledUrl).toContain("to=2026-06-30");
    expect(calledUrl).toContain("search=503");
  });

  it("omits from/to/search keys when they are undefined or empty", async () => {
    // Optional params must NOT be forwarded when absent — an empty `from`
    // would otherwise override the handler's current-month default (Pitfall 6).
    const payload: AuditResponse = { items: [], limit: 50, offset: 0, total: 0 };
    const fetchMock = mockFetchOnce(payload);
    vi.stubGlobal("fetch", fetchMock);

    await fetchAudit(50, 0);

    const calledUrl = fetchMock.mock.calls[0][0] as string;
    expect(calledUrl).toBe("/api/gateway/audit?limit=50&offset=0");
    expect(calledUrl).not.toContain("from=");
    expect(calledUrl).not.toContain("to=");
    expect(calledUrl).not.toContain("search=");
  });
});

describe("fetchUsage", () => {
  it("hits /api/gateway/usage with tenant + from + to query params", async () => {
    // This payload is the ACTUAL `admin.UsageResponse` shape the Go
    // handler emits (gateway/internal/admin/usage.go) — verified
    // field-for-field: tenant{id,slug,name,data_class,mode},
    // range{from,to,granularity,timezone}, summary{9 cost/usage fields},
    // rows[] of DayRow. The handler requires exactly tenant/from/to (400s
    // when any is missing) and parses from/to as YYYY-MM-DD — which is
    // exactly what fetchUsage sends (WR-01).
    const payload = {
      tenant: {
        id: "8f1c0d2e-4a5b-6c7d-8e9f-0a1b2c3d4e5f",
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
      rows: [
        {
          date: "2026-05-01",
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
      ],
    };
    const fetchMock = mockFetchOnce(payload);
    vi.stubGlobal("fetch", fetchMock);

    const result = await fetchUsage("converseai", "2026-05-01", "2026-05-14");

    const calledUrl = fetchMock.mock.calls[0][0] as string;
    expect(calledUrl.startsWith("/api/gateway/usage?")).toBe(true);
    expect(calledUrl).toContain("tenant=converseai");
    expect(calledUrl).toContain("from=2026-05-01");
    expect(calledUrl).toContain("to=2026-05-14");
    expect(result.tenant.slug).toBe("converseai");
    expect(result.range.timezone).toBe("America/Sao_Paulo");
    expect(result.summary.cost_total_brl).toBe(2.0);
    expect(result.rows[0].date).toBe("2026-05-01");
    expect(result.rows[0].cost_total_brl).toBe(2.0);
  });
});

describe("fetchEconomy", () => {
  it("hits /api/gateway/economy with from + to query params", async () => {
    // This payload is the ACTUAL `admin.EconomyResponse` shape the Go
    // handler emits (gateway/internal/admin/economy.go) — verified
    // field-for-field: range{from,to,timezone}, summary{7 fields with
    // roi_multiplier + pct_servido_local nullable}, series[] of day rows.
    // The handler parses from/to as YYYY-MM-DD — exactly what fetchEconomy
    // sends. NO per-tenant fan-out: a single server-side gateway-wide sum.
    const payload: EconomyResponse = {
      range: {
        from: "2026-06-01",
        to: "2026-06-30",
        timezone: "America/Sao_Paulo",
      },
      summary: {
        phantom_brl: 120.5,
        vast_brl: 30.0,
        economia_liquida_brl: 90.5,
        roi_multiplier: 4.0166,
        custo_openrouter_brl: 12.34,
        pct_servido_local: 0.87,
        horas_pod_up: 56.5,
      },
      series: [
        {
          date: "2026-06-01",
          phantom_brl: 10.0,
          vast_brl: 2.5,
          economia_brl: 7.5,
        },
      ],
    };
    const fetchMock = mockFetchOnce(payload);
    vi.stubGlobal("fetch", fetchMock);

    const result = await fetchEconomy("2026-06-01", "2026-06-30");

    const calledUrl = fetchMock.mock.calls[0][0] as string;
    expect(calledUrl.startsWith("/api/gateway/economy?")).toBe(true);
    expect(calledUrl).toContain("from=2026-06-01");
    expect(calledUrl).toContain("to=2026-06-30");
    expect(result.range.timezone).toBe("America/Sao_Paulo");
    expect(result.summary.economia_liquida_brl).toBe(90.5);
    expect(result.summary.roi_multiplier).toBe(4.0166);
    expect(result.summary.pct_servido_local).toBe(0.87);
    expect(result.summary.horas_pod_up).toBe(56.5);
    expect(result.series[0].economia_brl).toBe(7.5);
  });

  it("round-trips a fixture with roi_multiplier AND pct_servido_local null", async () => {
    // The Go handler emits JSON null for both when their denominator is
    // zero (vast_brl == 0 / total_requests == 0) — never Inf/NaN. The TS
    // type must accept null for both fields.
    const payload: EconomyResponse = {
      range: { from: "2026-06-01", to: "2026-06-30", timezone: "America/Sao_Paulo" },
      summary: {
        phantom_brl: 0,
        vast_brl: 0,
        economia_liquida_brl: 0,
        roi_multiplier: null,
        custo_openrouter_brl: 0,
        pct_servido_local: null,
        horas_pod_up: 0,
      },
      series: [],
    };
    const fetchMock = mockFetchOnce(payload);
    vi.stubGlobal("fetch", fetchMock);

    const result = await fetchEconomy("2026-06-01", "2026-06-30");

    expect(result.summary.roi_multiplier).toBeNull();
    expect(result.summary.pct_servido_local).toBeNull();
    expect(result.series).toHaveLength(0);
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

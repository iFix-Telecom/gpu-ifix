import { afterEach, describe, expect, it, vi } from "vitest";

import { GatewayError } from "@/lib/gateway";
import { gatewayAdminGet } from "@/lib/gateway-admin";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllEnvs();
});

describe("gatewayAdminGet — direct server-side admin read", () => {
  it("GETs ${base}/admin/<path> with X-Admin-Key and returns parsed JSON", async () => {
    vi.stubEnv("GATEWAY_BASE_URL", "http://gateway:8080");
    vi.stubEnv("GATEWAY_ADMIN_KEY", "ifix_admin_test");
    const body = [{ id: "1", slug: "t", name: "T" }];
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );

    const res = await gatewayAdminGet<typeof body>("tenants");

    expect(res).toEqual(body);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("http://gateway:8080/admin/tenants");
    expect(new Headers(init?.headers).get("X-Admin-Key")).toBe(
      "ifix_admin_test",
    );
    // Never hair-pins through the browser-facing proxy.
    expect(String(url)).not.toContain("/api/gateway/");
  });

  it("throws GatewayError with the gateway envelope message on non-2xx", async () => {
    vi.stubEnv("GATEWAY_BASE_URL", "http://gateway:8080");
    vi.stubEnv("GATEWAY_ADMIN_KEY", "k");
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ error: { message: "boom", type: "x" } }), {
        status: 502,
        headers: { "Content-Type": "application/json" },
      }),
    );

    await expect(gatewayAdminGet("tenants")).rejects.toBeInstanceOf(
      GatewayError,
    );
  });

  it("throws a configuration GatewayError when env is unset", async () => {
    vi.stubEnv("GATEWAY_BASE_URL", "");
    vi.stubEnv("GATEWAY_ADMIN_KEY", "");
    await expect(gatewayAdminGet("tenants")).rejects.toBeInstanceOf(
      GatewayError,
    );
  });
});

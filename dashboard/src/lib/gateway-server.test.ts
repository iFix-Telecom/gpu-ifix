import { afterEach, describe, expect, it, vi } from "vitest";

// gateway-server now reads the gateway DIRECTLY via gatewayAdminGet (no
// self-fetch hairpin through the public proxy URL — that redirected to /login
// and made res.json() throw on HTML). Mock the direct reader and assert
// delegation + path.
const { gatewayAdminGetMock } = vi.hoisted(() => ({
  gatewayAdminGetMock: vi.fn(),
}));
vi.mock("@/lib/gateway-admin", () => ({
  gatewayAdminGet: gatewayAdminGetMock,
}));

import { fetchPodConfigServer, fetchTenantsServer } from "@/lib/gateway-server";

afterEach(() => {
  vi.clearAllMocks();
});

describe("gateway-server — direct server-side gateway reads", () => {
  it("fetchPodConfigServer reads /admin/primary/config directly (no self-fetch)", async () => {
    const body = { config: {}, bounds: {} };
    gatewayAdminGetMock.mockResolvedValue(body);

    const res = await fetchPodConfigServer();

    expect(res).toEqual(body);
    expect(gatewayAdminGetMock).toHaveBeenCalledWith("primary/config");
  });

  it("fetchTenantsServer reads /admin/tenants directly", async () => {
    const rows = [{ id: "1", slug: "t", name: "T" }];
    gatewayAdminGetMock.mockResolvedValue(rows);

    const res = await fetchTenantsServer();

    expect(res).toEqual(rows);
    expect(gatewayAdminGetMock).toHaveBeenCalledWith("tenants");
  });

  it("propagates errors from the direct reader", async () => {
    gatewayAdminGetMock.mockRejectedValue(new Error("boom"));
    await expect(fetchTenantsServer()).rejects.toThrow("boom");
  });
});

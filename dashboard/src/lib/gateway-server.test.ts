import { afterEach, describe, expect, it, vi } from "vitest";

import { GatewayError } from "@/lib/gateway";

// Mutable header bag the mocked next/headers `headers()` returns.
let inboundHeaders = new Headers();
vi.mock("next/headers", () => ({
  headers: async () => inboundHeaders,
}));

import { fetchPodConfigServer } from "@/lib/gateway-server";

const okBody = { config: {}, bounds: {} };

afterEach(() => {
  vi.restoreAllMocks();
  inboundHeaders = new Headers();
});

describe("fetchPodConfigServer — RSC self-fetch", () => {
  it("forwards the inbound session Cookie to the proxy (else middleware bounces to /login HTML)", async () => {
    inboundHeaders = new Headers({
      host: "dash.example",
      "x-forwarded-proto": "https",
      cookie: "better-auth.session_token=abc; better-auth.session_data=xyz",
    });
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify(okBody), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );

    const res = await fetchPodConfigServer();
    expect(res).toEqual(okBody);

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("https://dash.example/api/gateway/primary/config");
    const sent = new Headers(init?.headers);
    expect(sent.get("Cookie")).toBe(
      "better-auth.session_token=abc; better-auth.session_data=xyz",
    );
  });

  it("omits the Cookie header when the inbound request has none", async () => {
    inboundHeaders = new Headers({ host: "dash.example" });
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify(okBody), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );

    await fetchPodConfigServer();
    const [, init] = fetchMock.mock.calls[0];
    expect(new Headers(init?.headers).has("Cookie")).toBe(false);
  });

  it("throws GatewayError with the envelope message on non-2xx", async () => {
    inboundHeaders = new Headers({ host: "dash.example" });
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ error: { message: "boom", type: "x" } }), {
        status: 502,
        headers: { "Content-Type": "application/json" },
      }),
    );
    await expect(fetchPodConfigServer()).rejects.toBeInstanceOf(GatewayError);
  });
});

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import LoginPage from "@/app/login/page";

/**
 * Regression guard for the login redirect contract (QUICK-2FA-REDIRECT).
 *
 * handleSubmit must branch on the better-auth twoFactorClient signal:
 *  - 2FA enabled  → signIn.email's onSuccess sees `data.twoFactorRedirect`
 *                   → router.push("/2fa/challenge") and NEVER "/"
 *  - happy path   → router.push("/")
 *  - error        → no navigation + "E-mail ou senha inválidos." surfaces
 *
 * `signIn.email` and `next/navigation` are mocked so no auth/network call runs.
 * The mock invokes the second-arg `onSuccess({ data })` exactly the way
 * better-auth's client does, so the component's narrowing path is exercised.
 */

const { signInEmailMock, pushMock, refreshMock } = vi.hoisted(() => ({
  signInEmailMock: vi.fn(),
  pushMock: vi.fn(),
  refreshMock: vi.fn(),
}));

vi.mock("@/lib/auth-client", () => ({
  signIn: { email: signInEmailMock },
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock, refresh: refreshMock }),
  useSearchParams: () => new URLSearchParams(),
}));

afterEach(() => {
  vi.clearAllMocks();
});

function submitLogin() {
  render(<LoginPage />);
  fireEvent.change(screen.getByLabelText("E-mail"), {
    target: { value: "ops@ifixtelecom.com.br" },
  });
  fireEvent.change(screen.getByLabelText("Senha"), {
    target: { value: "hunter2" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Entrar" }));
}

describe("LoginPage redirect contract", () => {
  it("routes to /2fa/challenge when the backend returns twoFactorRedirect", async () => {
    // Mirror better-auth: it resolves with no error AND fires onSuccess with the
    // narrowed { twoFactorRedirect } union.
    signInEmailMock.mockImplementation(async (_body, opts) => {
      await opts?.onSuccess?.({
        data: { twoFactorRedirect: true, twoFactorMethods: ["totp"] },
      });
      return { data: { twoFactorRedirect: true }, error: null };
    });

    submitLogin();

    await waitFor(() =>
      expect(pushMock).toHaveBeenCalledWith("/2fa/challenge"),
    );
    expect(pushMock).not.toHaveBeenCalledWith("/");
  });

  it("routes to / on the non-2FA happy path", async () => {
    signInEmailMock.mockImplementation(async (_body, opts) => {
      await opts?.onSuccess?.({ data: {} });
      return { data: {}, error: null };
    });

    submitLogin();

    await waitFor(() => expect(pushMock).toHaveBeenCalledWith("/"));
    expect(pushMock).not.toHaveBeenCalledWith("/2fa/challenge");
  });

  it("does not navigate and surfaces the error copy on a sign-in error", async () => {
    signInEmailMock.mockResolvedValue({
      data: null,
      error: { message: "invalid credentials" },
    });

    submitLogin();

    await waitFor(() =>
      expect(
        screen.getByText(/E-mail ou senha inválidos\./),
      ).toBeInTheDocument(),
    );
    expect(pushMock).not.toHaveBeenCalled();
  });
});

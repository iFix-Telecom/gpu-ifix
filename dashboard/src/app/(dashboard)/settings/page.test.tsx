/**
 * RED test stub for the self-service change-password page (UM-01, Phase 13).
 *
 * Mirrors `src/app/login/page.test.tsx`: mocks `@/lib/auth-client`
 * (`authClient.changePassword`) and `next/navigation` so no auth/network
 * call runs, then exercises the component's submit path.
 *
 * Contract (UM-01 / T-13-selfpw):
 *   - Requires the operator's CURRENT password (built-in
 *     `/change-password` endpoint, body { currentPassword, newPassword }).
 *   - Wrong current password → inline error code surfaces, no navigation,
 *     and the action is NOT audited (D-09 — asserted in admin-actions.test).
 *   - Success → fields cleared.
 *
 * EXPECTED TO FAIL (RED) until Wave 3 implements
 * `src/app/settings/page.tsx`. The page does not exist yet, so the test
 * does a guarded dynamic import of the default export and asserts it
 * resolved to a component — a missing page fails as an ASSERTION (RED),
 * NOT a vitest collection/import error.
 */
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const { changePasswordMock, pushMock, refreshMock } = vi.hoisted(() => ({
  changePasswordMock: vi.fn(),
  pushMock: vi.fn(),
  refreshMock: vi.fn(),
}));

vi.mock("@/lib/auth-client", () => ({
  authClient: { changePassword: changePasswordMock },
  changePassword: changePasswordMock,
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock, refresh: refreshMock }),
  useSearchParams: () => new URLSearchParams(),
}));

afterEach(() => {
  vi.clearAllMocks();
});

/**
 * Guarded dynamic import of the not-yet-built settings page component.
 *
 * The specifier is built from a variable so Vite's static
 * `import-analysis` does NOT resolve it at transform time (a literal
 * import of the not-yet-created page fails the whole SUITE at collection
 * rather than as a RED assertion — see 13-01 acceptance criteria). At
 * runtime the missing page rejects and is caught → FAILING ASSERTION.
 */
async function loadSettingsPage(): Promise<React.ComponentType | null> {
  const specifier = ["@/app/(dashboard)/settings", "page"].join("/");
  try {
    const mod = (await import(/* @vite-ignore */ specifier)) as {
      default?: React.ComponentType;
    };
    return mod.default ?? null;
  } catch {
    return null;
  }
}

describe("Settings self-service change-password (RED until Wave 3)", () => {
  it("surfaces an inline error and does not navigate when current password is wrong", async () => {
    const SettingsPage = await loadSettingsPage();
    // RED: page absent today → fails here as an assertion, not import error.
    expect(
      SettingsPage,
      "@/app/(dashboard)/settings/page must default-export a change-password form",
    ).not.toBeNull();
    if (!SettingsPage) return;

    changePasswordMock.mockResolvedValue({
      data: null,
      error: { message: "invalid current password", code: "INVALID_PASSWORD" },
    });

    render(<SettingsPage />);
    fireEvent.change(screen.getByLabelText(/senha atual/i), {
      target: { value: "wrongCurrent!1" },
    });
    fireEvent.change(screen.getByLabelText(/nova senha/i), {
      target: { value: "BrandNew!123" },
    });
    fireEvent.click(screen.getByRole("button", { name: /alterar senha/i }));

    await waitFor(() =>
      expect(screen.getByText(/senha atual.*inv|inválid/i)).toBeInTheDocument(),
    );
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("clears the fields on a successful change", async () => {
    const SettingsPage = await loadSettingsPage();
    expect(SettingsPage).not.toBeNull();
    if (!SettingsPage) return;

    changePasswordMock.mockResolvedValue({ data: {}, error: null });

    render(<SettingsPage />);
    const current = screen.getByLabelText(/senha atual/i) as HTMLInputElement;
    const next = screen.getByLabelText(/nova senha/i) as HTMLInputElement;
    fireEvent.change(current, { target: { value: "OldPassword!1" } });
    fireEvent.change(next, { target: { value: "NewPassword!1" } });
    fireEvent.click(screen.getByRole("button", { name: /alterar senha/i }));

    await waitFor(() => expect(changePasswordMock).toHaveBeenCalled());
    await waitFor(() => expect(current.value).toBe(""));
    expect(next.value).toBe("");
  });
});

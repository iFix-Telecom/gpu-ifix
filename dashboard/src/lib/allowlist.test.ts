import { describe, expect, it } from "vitest";
import { isAllowedEmail } from "@/lib/allowlist";

/**
 * D-13 domain allowlist tests. Behavior contract — env-driven via
 * DASHBOARD_ALLOWED_EMAIL_DOMAINS (comma-separated, lowercase); default
 * "ifixtelecom.com.br". `lastIndexOf("@")` (NOT regex) per
 * 11-RESEARCH §Don't Hand-Roll.
 */
describe("isAllowedEmail", () => {
  it("accepts @ifixtelecom.com.br", () => {
    expect(isAllowedEmail("admin@ifixtelecom.com.br")).toBe(true);
  });

  it("rejects non-allowlisted domains", () => {
    expect(isAllowedEmail("user@gmail.com")).toBe(false);
  });

  it("rejects malformed input", () => {
    expect(isAllowedEmail("no-at-sign")).toBe(false);
    expect(isAllowedEmail("")).toBe(false);
  });

  it("is case-insensitive", () => {
    expect(isAllowedEmail("Admin@IFIXTELECOM.COM.BR")).toBe(true);
  });
});

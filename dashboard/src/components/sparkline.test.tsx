import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Sparkline } from "@/components/sparkline";

/**
 * The sparkline is an ornament with one hard rule: it must not manufacture a
 * trend. Two or more points → a polyline; fewer → nothing at all (a flat line
 * would read as "steady", which is a claim the data does not support).
 */

describe("Sparkline", () => {
  it("renders a polyline with one vertex per point", () => {
    const { container } = render(<Sparkline points={[3, 9, 1, 7, 5]} />);
    const polyline = container.querySelector("polyline");
    expect(polyline).not.toBeNull();
    expect(polyline?.getAttribute("points")?.trim().split(/\s+/)).toHaveLength(
      5,
    );
  });

  it("renders nothing with fewer than two points", () => {
    const single = render(<Sparkline points={[42]} />);
    expect(single.container.querySelector("svg")).toBeNull();
    single.unmount();

    const empty = render(<Sparkline points={[]} />);
    expect(empty.container.querySelector("svg")).toBeNull();
  });
});

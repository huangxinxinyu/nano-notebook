import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { MaterialSymbol } from "./material-symbol";

test("hides decorative symbols from assistive technology", () => {
  const { container } = render(<MaterialSymbol name="settings" />);

  const symbol = container.querySelector(".material-symbol");
  expect(symbol).toHaveAttribute("aria-hidden", "true");
  expect(symbol).not.toHaveAttribute("aria-label");
});

test("exposes labeled symbols as images and configures variable font axes", () => {
  render(
    <MaterialSymbol
      name="search"
      label="Search"
      family="outlined"
      size={20}
      opticalSize={24}
      weight={500}
      fill
      grade={-25}
    />
  );

  const symbol = screen.getByRole("img", { name: "Search" });
  expect(symbol).toHaveClass("material-symbol", "material-symbol--outlined");
  expect(symbol).toHaveStyle({ fontSize: "20px" });
  expect(symbol.getAttribute("style")).toContain("--symbol-opsz: 24");
  expect(symbol.getAttribute("style")).toContain("--symbol-weight: 500");
  expect(symbol.getAttribute("style")).toContain("--symbol-fill: 1");
  expect(symbol.getAttribute("style")).toContain("--symbol-grade: -25");
});

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { expect, test } from "vitest";

const css = readFileSync(resolve(process.cwd(), "src/styles.css"), "utf8");

test("defines the centralized dark notebook theme", () => {
  const semanticTokens = [
    "--app-background",
    "--header-background",
    "--panel-background",
    "--panel-background-subtle",
    "--control-background",
    "--control-background-hover",
    "--selected-background",
    "--primary-foreground",
    "--secondary-foreground",
    "--muted-foreground",
    "--border",
    "--border-subtle",
    "--inverse-background",
    "--inverse-foreground",
    "--focus-ring",
    "--panel-radius",
    "--control-radius",
    "--content-max-width",
    "--header-height",
    "--workspace-gap"
  ];

  for (const token of semanticTokens) {
    expect(css).toContain(`${token}:`);
  }

  expect(css).toContain("color-scheme: dark");
  expect(css).toContain('font-family: "Google Sans Text", "Google Sans", Roboto, Arial, sans-serif');
  expect(css).not.toContain("linear-gradient");
});

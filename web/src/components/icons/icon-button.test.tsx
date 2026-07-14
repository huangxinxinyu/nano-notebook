import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test } from "vitest";
import { IconButton } from "./icon-button";

test("provides an accessible name and Material Symbol tooltip", async () => {
  render(<IconButton icon="search" label="Search notebooks" />);
  const user = userEvent.setup();

  const button = screen.getByRole("button", { name: "Search notebooks" });
  expect(button.querySelector(".material-symbol")).toBeInTheDocument();
  expect(button.querySelector("svg")).not.toBeInTheDocument();

  await user.hover(button);
  expect(await screen.findByRole("tooltip")).toHaveTextContent("Search notebooks");
});

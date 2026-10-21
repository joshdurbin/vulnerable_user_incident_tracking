import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { FilterBar } from "./FilterBar";

function Where() {
  const l = useLocation();
  return <output aria-label="url">{l.search}</output>;
}

const mount = (path: string, search = true) =>
  render(<MemoryRouter initialEntries={[path]}><FilterBar search={search} /><Where /></MemoryRouter>);

describe("search box", () => {
  it("puts the text in the URL on submit, not on every keystroke", async () => {
    mount("/?state=CA");
    const box = screen.getByRole("searchbox");
    await userEvent.type(box, "fatal cyclist");
    expect(screen.getByLabelText("url")).toHaveTextContent("?state=CA");
    await userEvent.type(box, "{Enter}");
    expect(screen.getByLabelText("url").textContent).toContain("q=fatal+cyclist");
    expect(screen.getByLabelText("url").textContent).toContain("state=CA");
  });

  it("shows the text from a shared link and clears it", async () => {
    mount("/?q=telegraph");
    expect(screen.getByRole("searchbox")).toHaveValue("telegraph");
    await userEvent.click(screen.getByRole("button", { name: "Clear" }));
    expect(screen.getByLabelText("url").textContent).not.toContain("q=");
    expect(screen.getByRole("searchbox")).toHaveValue("");
  });

  it("is absent where the page cannot use it", () => {
    mount("/", false);
    expect(screen.queryByRole("searchbox")).toBeNull();
  });
});

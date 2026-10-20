// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { createActionSheetPresenter } from "./action-sheet";

describe("mobile action sheets", () => {
  const appearance = {
    colorScheme: "dark" as const,
    foreground: "#ffffff",
    background: "#111111",
    mutedForeground: "#aaaaaa",
    destructive: "#ff0000",
  };

  it("supports long menus above modal routes and preserves destructive actions", () => {
    const show = vi.fn();
    const present = createActionSheetPresenter(show, appearance);
    const options = {
      options: ["Reply", "React", "Copy", "Select", "Resolve", "Delete", "Cancel"],
      cancelButtonIndex: 6,
      destructiveButtonIndex: 5,
    };
    const selected = vi.fn();
    present(options, selected);

    expect(show).toHaveBeenCalledWith(expect.objectContaining({
      ...options,
      useModal: true,
      userInterfaceStyle: "dark",
      tintColor: "#ffffff",
      containerStyle: { backgroundColor: "#111111" },
      destructiveColor: "#ff0000",
    }), expect.any(Function));
    expect(selected).not.toHaveBeenCalled();
    show.mock.calls[0][1](5);
    expect(selected).toHaveBeenCalledWith(5);
  });

  it.each([0, 3])("maps dismissal to cancel at index %s, including pressed-state cleanup", (cancelButtonIndex) => {
    const show = vi.fn();
    const present = createActionSheetPresenter(show, appearance);
    let pressed = true;
    const action = vi.fn();
    present({
      options: ["First", "Second", "Third", "Fourth"],
      cancelButtonIndex,
    }, (index) => {
      pressed = false;
      if (index !== cancelButtonIndex) action(index);
    });

    show.mock.calls[0][1](undefined);
    expect(pressed).toBe(false);
    expect(action).not.toHaveBeenCalled();
  });
});

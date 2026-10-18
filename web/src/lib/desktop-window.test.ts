import { closeDesktopWindow, minimiseDesktopWindow,
  setDesktopWindowTheme, toggleDesktopWindowMaximised } from "./desktop-window";

describe("desktop window controls", () => {
  afterEach(() => {
    Reflect.deleteProperty(window, "runtime");
  });

  it("delegates only to the Wails runtime window methods", () => {
    const runtime = {
      Quit: vi.fn(),
      WindowMinimise: vi.fn(),
      WindowSetDarkTheme: vi.fn(),
      WindowSetLightTheme: vi.fn(),
      WindowToggleMaximise: vi.fn(),
    };
    Object.defineProperty(window, "runtime", { configurable: true, value: runtime });

    minimiseDesktopWindow();
    toggleDesktopWindowMaximised();
    setDesktopWindowTheme("light");
    setDesktopWindowTheme("dark");
    setDesktopWindowTheme("glass");
    closeDesktopWindow();

    expect(runtime.WindowMinimise).toHaveBeenCalledTimes(1);
    expect(runtime.WindowToggleMaximise).toHaveBeenCalledTimes(1);
    expect(runtime.WindowSetLightTheme).toHaveBeenCalledTimes(2);
    expect(runtime.WindowSetDarkTheme).toHaveBeenCalledTimes(1);
    expect(runtime.Quit).toHaveBeenCalledTimes(1);
  });

  it("fails closed when the desktop runtime is absent", () => {
    expect(() => {
      minimiseDesktopWindow();
      toggleDesktopWindowMaximised();
      setDesktopWindowTheme("light");
      setDesktopWindowTheme("dark");
      setDesktopWindowTheme("glass");
      closeDesktopWindow();
    }).not.toThrow();
  });
});

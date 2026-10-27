import "@testing-library/jest-dom/vitest";

// jsdom does not implement these, and several components touch them during
// layout. Stubbing them keeps component tests from failing for reasons that
// have nothing to do with the code under test.
if (typeof window !== "undefined") {
  if (!window.matchMedia) {
    Object.defineProperty(window, "matchMedia", {
      writable: true,
      value: (query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addListener: () => {},
        removeListener: () => {},
        addEventListener: () => {},
        removeEventListener: () => {},
        dispatchEvent: () => false,
      }),
    });
  }

  if (!window.ResizeObserver) {
    class RO {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    window.ResizeObserver = RO;
  }

  if (!Element.prototype.scrollIntoView) {
    Element.prototype.scrollIntoView = function scrollIntoView() {};
  }
}

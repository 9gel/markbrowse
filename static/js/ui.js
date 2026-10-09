/* Theme switch and mermaid diagrams for markbrowse.
 *
 * Theme: the button cycles system -> light -> dark. The choice lives in
 * localStorage("mb-theme") and on <html data-theme>; an inline script in
 * <head> restores it before first paint.
 *
 * Mermaid: diagrams render in place. Tapping one opens it full-window,
 * where it pans (drag), zooms (wheel, pinch, +/- buttons) and closes on Esc,
 * the X button or the browser back gesture.
 */
(function () {
  "use strict";

  var root = document.documentElement;
  var modes = ["system", "light", "dark"];
  var icons = { system: "◐", light: "☀", dark: "☾" };
  var dark = window.matchMedia("(prefers-color-scheme: dark)");

  function mode() { return root.dataset.theme || "system"; }

  function isDark() {
    var m = mode();
    return m === "dark" || (m === "system" && dark.matches);
  }

  var btn = document.getElementById("theme-toggle");
  function paintButton() {
    if (!btn) return;
    btn.textContent = icons[mode()];
    btn.title = btn.ariaLabel = "Theme: " + mode();
  }
  paintButton();
  if (btn) {
    btn.onclick = function () {
      var next = modes[(modes.indexOf(mode()) + 1) % modes.length];
      if (next === "system") delete root.dataset.theme;
      else root.dataset.theme = next;
      try {
        if (next === "system") localStorage.removeItem("mb-theme");
        else localStorage.setItem("mb-theme", next);
      } catch (e) { /* private mode: choice lasts for this page only */ }
      paintButton();
      renderMermaid();
    };
  }
  dark.addEventListener("change", function () {
    if (mode() === "system") renderMermaid();
  });

  // --------------------------------------------------------------- mermaid

  if (!window.mermaid) return;
  var blocks = Array.prototype.slice.call(
    document.querySelectorAll("pre.mermaid"));
  blocks.forEach(function (el) {
    el.dataset.src = el.textContent;
    el.title = "Open full window";
    el.addEventListener("click", function () {
      var svg = el.querySelector("svg");
      if (svg) openViewer(svg);
    });
  });

  function renderMermaid() {
    blocks.forEach(function (el) {
      el.removeAttribute("data-processed");
      el.textContent = el.dataset.src;
    });
    mermaid.initialize({
      startOnLoad: false,
      securityLevel: "strict",
      theme: isDark() ? "dark" : "default"
    });
    mermaid.run({ nodes: blocks });
  }
  renderMermaid();

  function openViewer(svg) {
    var box = svg.viewBox && svg.viewBox.baseVal;
    var w = box && box.width ? box.width : svg.getBoundingClientRect().width;
    var h = box && box.height ? box.height
      : svg.getBoundingClientRect().height;

    var overlay = document.createElement("div");
    overlay.className = "mermaid-viewer";
    overlay.setAttribute("role", "dialog");
    overlay.setAttribute("aria-label", "Diagram");

    // Panzoom transforms a wrapper div; transforming the <svg> itself is
    // handled inconsistently across browsers.
    var copy = svg.cloneNode(true);
    copy.removeAttribute("style");
    copy.setAttribute("width", w);
    copy.setAttribute("height", h);
    var stage = document.createElement("div");
    stage.className = "mermaid-stage";
    stage.appendChild(copy);
    overlay.appendChild(stage);

    var bar = document.createElement("div");
    bar.className = "mermaid-viewer-bar";
    function tool(label, title, fn) {
      var b = document.createElement("button");
      b.type = "button";
      b.textContent = label;
      b.title = b.ariaLabel = title;
      b.onclick = fn;
      bar.appendChild(b);
    }
    overlay.appendChild(bar);
    document.body.appendChild(overlay);
    document.body.classList.add("viewer-open");

    // Panzoom's focal-point zoom assumes the stage sits at the overlay's
    // top-left with its default 50% origin; flex centering breaks that math.
    // Centre with the start pan instead. Scaling about the stage centre c,
    // screen = c + scale * (point - c + pan).
    var W = overlay.clientWidth, H = overlay.clientHeight;
    var fit = Math.min(W / w, H / h) * 0.95;
    var pz = Panzoom(stage, {
      canvas: true,
      startScale: fit,
      startX: (W - w) / (2 * fit),
      startY: (H - h) / (2 * fit),
      minScale: fit / 4,
      maxScale: Math.max(fit * 40, 4)
    });
    overlay.addEventListener("wheel", pz.zoomWithWheel, { passive: false });

    tool("+", "Zoom in", function () { pz.zoomIn(); });
    tool("−", "Zoom out", function () { pz.zoomOut(); });
    tool("↺", "Fit", function () { pz.reset(); });
    tool("✕", "Close", function () { history.back(); });

    // A history entry so the phone back gesture closes the viewer instead
    // of leaving the page.
    history.pushState({ mbViewer: true }, "");
    function close() {
      window.removeEventListener("popstate", close);
      document.removeEventListener("keydown", onKey);
      pz.destroy();
      overlay.remove();
      document.body.classList.remove("viewer-open");
    }
    function onKey(e) { if (e.key === "Escape") history.back(); }
    window.addEventListener("popstate", close);
    document.addEventListener("keydown", onKey);
  }
})();

// terminatr.dev: small touches only. The page works without this file.
(function () {
  "use strict";

  var doc = document.documentElement;
  var still = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  // The real width of a monospace cell, so the screens fit their frames.
  var probe = document.createElement("span");
  probe.style.cssText = "position:absolute;visibility:hidden;white-space:pre;font:100px/1 var(--mono)";
  probe.textContent = "0000000000";
  document.body.appendChild(probe);
  var cw = probe.getBoundingClientRect().width / 1000;
  probe.remove();
  if (cw > 0.4 && cw < 0.8) doc.style.setProperty("--cw", cw.toFixed(4));

  // Type `tm` into the hero's prompt.
  var cmd = document.querySelector(".typed .cmd");
  if (cmd) {
    var text = cmd.getAttribute("data-type") || "";
    if (still) {
      cmd.textContent = text;
    } else {
      var i = 0;
      var step = function () {
        cmd.textContent = text.slice(0, ++i);
        if (i < text.length) setTimeout(step, 140);
      };
      setTimeout(step, 600);
    }
  }

  // Screen tabs (WAI-ARIA tabs: arrows, Home and End move between them).
  var tabs = Array.prototype.slice.call(document.querySelectorAll('[role="tab"]'));
  function select(tab, focus) {
    tabs.forEach(function (t) {
      var on = t === tab;
      t.setAttribute("aria-selected", on ? "true" : "false");
      t.tabIndex = on ? 0 : -1;
      document.getElementById(t.getAttribute("aria-controls")).hidden = !on;
    });
    if (focus) tab.focus();
  }
  tabs.forEach(function (tab, n) {
    tab.addEventListener("click", function () { select(tab, false); });
    tab.addEventListener("keydown", function (e) {
      var to = null;
      if (e.key === "ArrowRight") to = tabs[(n + 1) % tabs.length];
      else if (e.key === "ArrowLeft") to = tabs[(n - 1 + tabs.length) % tabs.length];
      else if (e.key === "Home") to = tabs[0];
      else if (e.key === "End") to = tabs[tabs.length - 1];
      if (to) { e.preventDefault(); select(to, true); }
    });
  });

  // Copy buttons.
  document.querySelectorAll(".copy").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var code = btn.parentNode.querySelector("code").textContent;
      var done = function (ok) {
        btn.textContent = ok ? "copied" : "select";
        btn.classList.toggle("done", ok);
        setTimeout(function () { btn.textContent = "copy"; btn.classList.remove("done"); }, 1600);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(code).then(function () { done(true); }, function () { done(false); });
      } else {
        done(false);
      }
    });
  });

  // Fade sections in as they scroll into view.
  if (!still && "IntersectionObserver" in window) {
    doc.classList.add("js");
    var seen = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (e.isIntersecting) { e.target.classList.add("in"); seen.unobserve(e.target); }
      });
    }, { rootMargin: "0px 0px -8% 0px" });
    document.querySelectorAll(".features li, .term, .steps li").forEach(function (el) {
      el.classList.add("reveal");
      seen.observe(el);
    });
  }
})();

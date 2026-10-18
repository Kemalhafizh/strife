// Site-wide behaviour: the mobile menu and copy buttons on code blocks.
(() => {
  "use strict";
  const doc = document.documentElement;
  const strings = { copy: doc.dataset.copy || "Copy", copied: doc.dataset.copied || "Copied" };

  const nav = document.querySelector(".nav");
  const toggle = nav && nav.querySelector(".nav-toggle");
  if (toggle) {
    const close = () => { nav.removeAttribute("data-open"); toggle.setAttribute("aria-expanded", "false"); };
    toggle.addEventListener("click", () => {
      const open = !nav.hasAttribute("data-open");
      nav.toggleAttribute("data-open", open);
      toggle.setAttribute("aria-expanded", String(open));
    });
    document.addEventListener("keydown", (e) => { if (e.key === "Escape") close(); });
    document.addEventListener("click", (e) => { if (!nav.contains(e.target)) close(); });
  }

  async function copyText(text, button) {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const area = document.createElement("textarea");
      area.value = text;
      area.setAttribute("readonly", "");
      area.style.position = "fixed";
      area.style.opacity = "0";
      document.body.appendChild(area);
      area.select();
      try { document.execCommand("copy"); } catch { /* nothing else to try */ }
      area.remove();
    }
    const label = button.textContent;
    button.textContent = strings.copied;
    setTimeout(() => { button.textContent = label; }, 1500);
  }

  function addCopy(block, getText) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "copy-btn";
    button.textContent = strings.copy;
    button.addEventListener("click", () => copyText(getText(), button));
    block.appendChild(button);
  }

  document.querySelectorAll(".prose .highlight, .prose > pre").forEach((block) => {
    const code = block.querySelector("code") || block;
    addCopy(block, () => code.innerText.replace(/\n$/, ""));
  });
  document.querySelectorAll("[data-copy-target]").forEach((button) => {
    button.addEventListener("click", () => {
      const target = document.getElementById(button.dataset.copyTarget);
      if (target) copyText(target.innerText.trim(), button);
    });
  });

  window.StrifeSite = { copyText };
})();

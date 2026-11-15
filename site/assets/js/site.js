// Site-wide behaviour: responsive popover navigation and Invoker Command copy buttons.
(() => {
  "use strict";
  const doc = document.documentElement;
  const strings = {
    copy: doc.dataset.copy || "Copy",
    copied: doc.dataset.copied || "Copied",
    copyFailed: doc.dataset.copyFailed || "Copy failed",
  };

  // 1. Responsive Site Navigation (Popover when narrow, inline when wide)
  const header = document.querySelector(".site-header");
  const inner = header && header.querySelector(".header-inner");
  const menu = document.getElementById("site-menu");
  const menuBtn = header && header.querySelector(".menu-button");

  if (header && inner && menu) {
    const hasPopover = Object.hasOwn(HTMLElement.prototype, "popover");
    let lastHeaderWidth = header.clientWidth;

    const syncNavLayout = () => {
      let wasOpen = false;
      try {
        wasOpen = menu.matches(":popover-open") || menu.classList.contains(":popover-open");
      } catch {
        wasOpen = menu.classList.contains(":popover-open");
      }

      header.classList.add("is-measuring", "is-wide");
      menu.removeAttribute("popover");

      const fits = inner.scrollWidth <= inner.clientWidth;

      if (fits) {
        header.classList.add("is-wide");
        menu.removeAttribute("popover");
        menu.classList.remove(":popover-open");
      } else {
        header.classList.remove("is-wide");
        menu.setAttribute("popover", "auto");
        if (wasOpen && typeof menu.showPopover === "function") {
          try { menu.showPopover(); } catch { /* ignore */ }
        }
      }

      lastHeaderWidth = header.clientWidth;
      requestAnimationFrame(() => {
        header.classList.remove("is-measuring");
      });
    };

    if (!hasPopover && menuBtn) {
      menuBtn.addEventListener("click", () => {
        if (header.classList.contains("is-wide")) return;
        menu.classList.toggle(":popover-open");
      });
    }

    syncNavLayout();

    if ("ResizeObserver" in window) {
      const ro = new ResizeObserver(() => {
        const isWide = header.classList.contains("is-wide");
        const currentWidth = header.clientWidth;
        if (isWide || currentWidth > lastHeaderWidth) {
          syncNavLayout();
        } else {
          lastHeaderWidth = currentWidth;
        }
      });
      ro.observe(header);
      ro.observe(menu);
    }
  }

  // 2. Copy Button (Invoker Commands + Async Clipboard + Accessible Live Region)
  const timers = new WeakMap();
  let lastMessage = "";

  const copyIconsSvg =
    '<svg class="icon icon-copy" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    '<rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect>' +
    '<path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>' +
    '<svg class="icon icon-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    '<polyline points="20 6 9 17 4 12"></polyline></svg>';

  function legacyCopy(text) {
    const field = Object.assign(document.createElement("textarea"), { value: text });
    field.setAttribute("readonly", "");
    field.style.cssText = "position:fixed;inset-block-start:0;opacity:0";
    document.body.append(field);
    field.select();
    try { return document.execCommand("copy"); } finally { field.remove(); }
  }

  function report(button, ok) {
    const tip = button.nextElementSibling;
    clearTimeout(timers.get(button));
    button.removeAttribute("data-copied");
    button.removeAttribute("data-failed");
    let message = ok ? strings.copied : strings.copyFailed;
    if (message === lastMessage) message += "\u00A0";
    lastMessage = message;

    if (tip) tip.textContent = "";
    void button.offsetWidth;
    button.setAttribute(ok ? "data-copied" : "data-failed", "");
    if (tip) {
      requestAnimationFrame(() => (tip.textContent = message));
    }
    timers.set(button, setTimeout(() => {
      button.removeAttribute("data-copied");
      button.removeAttribute("data-failed");
      if (tip) tip.textContent = "";
    }, 2000));
  }

  async function copy(button, target) {
    if (!target) return;
    const text = target.matches("input, textarea")
      ? target.value
      : (target.innerText || target.textContent || "").trim();
    if (!text) return;
    try {
      await navigator.clipboard.writeText(text);
      report(button, true);
    } catch {
      if (legacyCopy(text)) {
        report(button, true);
      } else {
        report(button, false);
      }
    }
  }

  async function copyText(text, button) {
    let ok = false;
    try {
      await navigator.clipboard.writeText(text);
      ok = true;
    } catch {
      ok = legacyCopy(text);
    }
    if (button) report(button, ok);
  }

  function createCopyWidget(targetId, labelText) {
    const wrap = document.createElement("span");
    wrap.className = "copy";

    const button = document.createElement("button");
    button.type = "button";
    button.className = "copy-button";
    button.setAttribute("command", "--copy");
    button.setAttribute("commandfor", targetId);
    button.setAttribute("aria-label", labelText || strings.copy);
    button.innerHTML = `${copyIconsSvg}<span class="copy-label">${strings.copy}</span>`;

    const tip = document.createElement("span");
    tip.className = "tip";
    tip.setAttribute("role", "status");

    wrap.append(button, tip);
    return wrap;
  }

  if ("CommandEvent" in window) {
    document.addEventListener("command", (event) => {
      if (event.command && event.command.startsWith("--copy")) {
        copy(event.source, event.target, event.command);
      }
    }, true);
  } else {
    document.addEventListener("click", (event) => {
      const button = event.target.closest('button[command^="--copy"]');
      if (!button) return;
      const target = document.getElementById(button.getAttribute("commandfor"));
      if (target) copy(button, target, button.getAttribute("command"));
    });
  }

  let seq = 0;
  document.querySelectorAll(".prose .highlight, .prose > pre").forEach((block) => {
    if (block.querySelector(".copy")) return;
    const code = block.querySelector("code") || block;
    if (!code.id) code.id = `code-snippet-${++seq}`;
    block.classList.add("copyable");
    block.appendChild(createCopyWidget(code.id, `${strings.copy} code`));
  });

  window.StrifeSite = { copyText, createCopyWidget };
})();

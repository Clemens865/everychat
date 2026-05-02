/**
 * Everychat embed loader.
 *
 * Customer pastes:
 *   <script src="https://everychat.host/embed.js" data-bot="<token>"></script>
 *
 * On load we:
 *   1. Read data-bot from the host script tag
 *   2. Define <everychat-launcher> with closed shadow DOM (so the customer's
 *      page CSS can't break the bubble)
 *   3. Append a <everychat-launcher> to the body
 *   4. On launcher click → toggle a fixed-position iframe pointing at
 *      https://everychat.host/widget/<token>/shell
 *
 * Cross-origin: the iframe origin === everychat.host, the customer page is
 * a different origin. They communicate via window.postMessage.
 */
(function () {
  "use strict";

  // Resolve our own origin so the iframe and asset URLs work regardless
  // of where the customer's page is served from.
  var hostScript = document.currentScript;
  if (!hostScript) {
    // Fallback: find any script tag pointing at /embed.js.
    var scripts = document.getElementsByTagName("script");
    for (var i = scripts.length - 1; i >= 0; i--) {
      if (/\/embed\.js(\?|$)/.test(scripts[i].src)) {
        hostScript = scripts[i];
        break;
      }
    }
  }
  if (!hostScript || !hostScript.src) {
    console.warn("[everychat] could not locate own script tag; aborting");
    return;
  }
  var botToken = (hostScript.dataset && hostScript.dataset.bot) || "";
  if (!botToken) {
    console.warn("[everychat] data-bot is missing on the script tag");
    return;
  }
  var origin = new URL(hostScript.src).origin;

  if (customElements.get("everychat-launcher")) {
    return; // already defined; idempotent for accidental double-include
  }

  customElements.define("everychat-launcher", class extends HTMLElement {
    connectedCallback() {
      var bot = this.getAttribute("data-bot") || botToken;
      var hostOrigin = this.getAttribute("data-origin") || origin;
      var accent = this.getAttribute("data-accent") || "#2f4cff";

      // Closed shadow DOM: customer page can't reach in.
      var shadow = this.attachShadow({ mode: "closed" });
      shadow.innerHTML =
        '<style>' +
          ':host { position: fixed; right: 24px; bottom: 24px; z-index: 2147483640; }' +
          'button.btn { width: 56px; height: 56px; border-radius: 50%; background: ' + accent + '; ' +
                      'color: #fff; border: 0; cursor: pointer; box-shadow: 0 8px 24px rgba(0,0,0,0.25); ' +
                      'font-size: 24px; line-height: 1; transition: transform 0.15s ease; }' +
          'button.btn:hover { transform: scale(1.05); }' +
          'iframe { position: fixed; right: 24px; bottom: 92px; width: 380px; height: 600px; ' +
                  'max-height: calc(100vh - 120px); border: 0; border-radius: 14px; ' +
                  'box-shadow: 0 18px 48px rgba(0,0,0,0.28), 0 4px 12px rgba(0,0,0,0.12); ' +
                  'background: #fff; display: none; }' +
          'iframe.open { display: block; }' +
          '@media (max-width: 480px) {' +
            ':host { right: 12px; bottom: 12px; }' +
            'iframe { right: 12px; bottom: 80px; width: calc(100vw - 24px); height: calc(100vh - 100px); }' +
          '}' +
        '</style>' +
        '<button class="btn" type="button" aria-label="Chat öffnen">💬</button>' +
        '<iframe title="Everychat" sandbox="allow-scripts allow-forms allow-same-origin allow-popups"></iframe>';

      var btn = shadow.querySelector("button.btn");
      var iframe = shadow.querySelector("iframe");
      var loaded = false;
      var open = false;

      var setOpen = function (next) {
        open = next;
        iframe.classList.toggle("open", open);
        btn.textContent = open ? "×" : "💬";
        btn.setAttribute("aria-label", open ? "Chat schließen" : "Chat öffnen");
        if (open && !loaded) {
          iframe.src = hostOrigin + "/widget/" + encodeURIComponent(bot) + "/shell";
          loaded = true;
        }
      };

      btn.addEventListener("click", function () { setOpen(!open); });

      // Iframe → parent messages (e.g. visitor clicked × inside).
      window.addEventListener("message", function (e) {
        if (e.origin !== hostOrigin) return;
        if (e.data && e.data.type === "everychat:close") setOpen(false);
      });
    }
  });

  // Mount once the body is ready.
  var mount = function () {
    if (document.querySelector("everychat-launcher")) return;
    var el = document.createElement("everychat-launcher");
    el.setAttribute("data-bot", botToken);
    el.setAttribute("data-origin", origin);
    document.body.appendChild(el);
  };
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", mount);
  } else {
    mount();
  }
})();

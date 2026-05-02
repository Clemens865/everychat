/**
 * Everychat in-iframe chat runtime.
 *
 * Posts visitor messages to /api/v1/chat (same origin as the iframe),
 * parses the SSE stream, and renders messages via DOMPurify-sanitized
 * HTML. The bot token is injected by the bubble.html template into
 * window.__BOT_TOKEN__ before this script runs.
 */
(function () {
  "use strict";

  var token = window.__BOT_TOKEN__;
  if (!token) {
    console.error("[everychat-runtime] missing window.__BOT_TOKEN__");
    return;
  }

  var body = document.getElementById("w-body");
  var form = document.getElementById("w-form");
  var input = document.getElementById("w-input");
  var send = document.getElementById("w-send");
  var closeBtn = document.getElementById("w-close");
  var starters = document.getElementById("w-starters");

  closeBtn.addEventListener("click", function () {
    // Tell the parent (embed.js launcher) to hide the iframe.
    window.parent.postMessage({ type: "everychat:close" }, "*");
  });

  if (starters) {
    starters.querySelectorAll(".w-starter").forEach(function (b) {
      b.addEventListener("click", function () {
        input.value = b.textContent.trim();
        form.dispatchEvent(new Event("submit", { cancelable: true }));
      });
    });
  }

  // Lightweight Markdown → HTML for chat-typical patterns. Deliberately
  // narrow: paragraphs, bold, italics, inline code, simple lists. Anything
  // we don't recognize falls through to a <p> with the raw text. DOMPurify
  // is the final gate — even if our markdown maps something weird, the
  // sanitizer's allowlist holds.
  function mdToHTML(src) {
    src = src.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
    // bold + italic + code (order matters: code first to protect contents)
    src = src.replace(/`([^`]+)`/g, "<code>$1</code>");
    src = src.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
    src = src.replace(/\*([^*]+)\*/g, "<em>$1</em>");
    // line-break aware paragraphs
    var blocks = src.split(/\n{2,}/).map(function (block) {
      var lines = block.split(/\n/).map(function (l) { return l.trim(); }).filter(Boolean);
      if (!lines.length) return "";
      // unordered list
      if (lines.every(function (l) { return /^[-*]\s+/.test(l); })) {
        return "<ul>" + lines.map(function (l) {
          return "<li>" + l.replace(/^[-*]\s+/, "") + "</li>";
        }).join("") + "</ul>";
      }
      return "<p>" + lines.join("<br>") + "</p>";
    });
    return blocks.join("\n");
  }

  function sanitize(html) {
    if (typeof DOMPurify === "undefined") return html;
    return DOMPurify.sanitize(html, {
      ALLOWED_TAGS: ["p", "br", "strong", "em", "code", "ul", "ol", "li", "a", "pre"],
      ALLOWED_ATTR: ["href", "target", "rel"],
      ALLOW_DATA_ATTR: false,
    });
  }

  function appendMsg(role, text) {
    var wrap = document.createElement("div");
    wrap.className = "w-msg w-msg--" + role;
    var bubble = document.createElement("div");
    bubble.className = "w-bubble";
    bubble.innerHTML = role === "bot" ? sanitize(mdToHTML(text)) : escapeText(text);
    wrap.appendChild(bubble);
    body.appendChild(wrap);
    body.scrollTop = body.scrollHeight;
    return { wrap: wrap, bubble: bubble };
  }

  function escapeText(s) {
    var d = document.createElement("div");
    d.textContent = s;
    return d.innerHTML;
  }

  function appendSources(parent, sources) {
    if (!sources || !sources.length) return;
    var s = document.createElement("div");
    s.className = "w-sources";
    var det = document.createElement("details");
    var sum = document.createElement("summary");
    sum.textContent = sources.length + " Quellen anzeigen";
    det.appendChild(sum);
    var ul = document.createElement("ul");
    sources.forEach(function (src) {
      var li = document.createElement("li");
      var code = document.createElement("code");
      code.textContent = src.source;
      li.appendChild(code);
      var ex = document.createElement("div");
      ex.textContent = src.excerpt;
      ex.style.color = "var(--w-muted)";
      ex.style.marginTop = "2px";
      li.appendChild(ex);
      ul.appendChild(li);
    });
    det.appendChild(ul);
    s.appendChild(det);
    parent.parentElement.appendChild(s);
  }

  form.addEventListener("submit", function (e) {
    e.preventDefault();
    var msg = input.value.trim();
    if (!msg) return;
    input.value = "";
    input.disabled = true;
    send.disabled = true;
    if (starters) starters.style.display = "none"; // hide starter prompts after first turn

    appendMsg("user", msg);
    var bot = appendMsg("bot", "");
    bot.bubble.innerHTML = '<span class="w-cursor"></span>';

    fetch("/api/v1/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ bot_token: token, message: msg }),
    }).then(function (res) {
      if (!res.ok) {
        return res.text().then(function (t) {
          bot.bubble.textContent = "Fehler: " + res.status + " " + (t || "");
          throw new Error("non-2xx");
        });
      }
      return streamSSE(res, bot);
    }).catch(function (err) {
      console.warn("[everychat-runtime] chat error", err);
    }).finally(function () {
      input.disabled = false;
      send.disabled = false;
      input.focus();
    });
  });

  function streamSSE(res, bot) {
    var reader = res.body.getReader();
    var decoder = new TextDecoder();
    var buffer = "";
    var answer = "";

    function pump() {
      return reader.read().then(function (chunk) {
        if (chunk.done) {
          if (!answer) bot.bubble.innerHTML = sanitize(mdToHTML("(keine Antwort)"));
          return;
        }
        buffer += decoder.decode(chunk.value, { stream: true });
        var idx;
        while ((idx = buffer.indexOf("\n\n")) >= 0) {
          var event = buffer.slice(0, idx);
          buffer = buffer.slice(idx + 2);
          var lines = event.split("\n");
          var ev = ((lines.find(function (l) { return l.indexOf("event:") === 0; })) || "").slice(6).trim();
          var data = lines.filter(function (l) { return l.indexOf("data:") === 0; })
                          .map(function (l) { return l.slice(5).replace(/^ /, ""); })
                          .join("\n");
          if (ev === "token") {
            answer += data + (data.endsWith("\n") ? "" : "");
            bot.bubble.innerHTML = sanitize(mdToHTML(answer)) + '<span class="w-cursor"></span>';
            body.scrollTop = body.scrollHeight;
          } else if (ev === "done") {
            bot.bubble.innerHTML = sanitize(mdToHTML(answer));
            try {
              var meta = JSON.parse(data);
              appendSources(bot.bubble, meta.sources);
            } catch (e) { /* ignore */ }
          } else if (ev === "error") {
            bot.bubble.textContent = "Fehler: " + data;
          }
        }
        return pump();
      });
    }
    return pump();
  }
})();

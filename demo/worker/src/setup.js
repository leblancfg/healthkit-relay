// The setup page, as server/setup.go renders it on a demo server: with the shared code it
// shows the pairing QR code and link; without it, the admin sign-in (which no token passes
// here: the Worker has no admin). The markup and style are setup.go's.

import { encodeQR, qrSVG } from "./qr.js";

export const AppScheme = "healthrelay";

// pairLink is PairLink in server/pair.go: url.Values encodes keys in sorted order.
export function pairLink(server, code) {
  return `${AppScheme}://pair?code=${formEncode(code)}&server=${formEncode(server)}`;
}

// formEncode is Go's url.QueryEscape.
function formEncode(s) {
  return encodeURIComponent(s).replace(/[!'()*]/g, (c) => "%" + c.charCodeAt(0).toString(16).toUpperCase()).replace(/%20/g, "+");
}

const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&#34;", "'": "&#39;" })[c]);
// jsString is a JavaScript string literal safe inside <script>, like html/template's.
const jsString = (s) => JSON.stringify(s).replace(/[<>&\u2028\u2029]/g, (c) => "\\u" + c.charCodeAt(0).toString(16).padStart(4, "0"));

const style = `
<style>
:root { --accent: #ff2d55; --bg: #f2f2f7; --card: #fff; --text: #1c1c1e; --muted: #6e6e73; --line: #e5e5ea; --ok: #34c759; }
@media (prefers-color-scheme: dark) { :root { --bg: #000; --card: #1c1c1e; --text: #f5f5f7; --muted: #98989d; --line: #38383a; } }
* { box-sizing: border-box; }
body { margin: 0; min-height: 100vh; background: var(--bg); color: var(--text); -webkit-font-smoothing: antialiased;
  font: 17px/1.45 -apple-system, BlinkMacSystemFont, "SF Pro Text", "Segoe UI", system-ui, sans-serif;
  display: flex; justify-content: center; padding: max(40px, env(safe-area-inset-top)) 16px 32px; }
main { width: 100%; max-width: 420px; }
header { display: flex; align-items: center; justify-content: center; gap: 10px; margin-bottom: 28px; }
header svg { width: 34px; height: 34px; }
header h1 { font-size: 24px; font-weight: 700; letter-spacing: -0.02em; margin: 0; }
.card { background: var(--card); border-radius: 22px; padding: 28px 24px; margin-bottom: 18px; text-align: center;
  box-shadow: 0 1px 2px rgba(0,0,0,.04), 0 8px 24px rgba(0,0,0,.05); }
h2 { font-size: 22px; font-weight: 700; letter-spacing: -0.01em; margin: 0 0 6px; }
.sub { color: var(--muted); margin: 0 0 22px; font-size: 15px; }
.qr { width: 236px; margin: 0 auto 18px; padding: 10px; background: #fff; border-radius: 16px; border: 1px solid var(--line); }
.qr svg { display: block; width: 100%; height: auto; }
.button { display: block; width: 100%; border: 0; border-radius: 14px; background: var(--accent); color: #fff;
  font: inherit; font-weight: 600; padding: 15px; text-decoration: none; cursor: pointer; }
.button:active { opacity: .8; }
.hint { color: var(--muted); font-size: 14px; margin: 14px 0 0; }
.status { display: inline-flex; align-items: center; gap: 8px; color: var(--muted); font-size: 14px; margin: 18px 0 0; }
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--accent); animation: pulse 1.6s ease-in-out infinite; }
@keyframes pulse { 50% { opacity: .25; } }
.phone { display: none; }
@media (pointer: coarse) { .phone { display: block; } .desktop { display: none; } }
span.desktop { display: inline; }
@media (pointer: coarse) { span.desktop { display: none; } }
.check { width: 64px; height: 64px; margin: 4px auto 14px; display: block; }
form.login { text-align: left; margin-top: 20px; }
label { display: block; font-size: 13px; font-weight: 600; color: var(--muted); text-transform: uppercase; letter-spacing: .04em; margin: 0 0 6px 4px; }
input[type=password] { width: 100%; font: inherit; padding: 13px 14px; border-radius: 12px; border: 1px solid var(--line);
  background: var(--bg); color: var(--text); margin-bottom: 14px; }
input[type=text] { font: inherit; font-size: 15px; padding: 8px 10px; border-radius: 10px; border: 1px solid var(--line); background: var(--bg); color: var(--text); }
code.token { white-space: normal; word-break: break-all; user-select: all; font-size: 15px; text-align: left; }
.secret { display: flex; align-items: center; justify-content: center; gap: 8px; margin: 14px 0 0; }
button.copy { flex: none; display: inline-flex; align-items: center; justify-content: center; width: 36px; height: 36px; padding: 0;
  border: 1px solid var(--line); border-radius: 10px; background: var(--bg); color: var(--text); cursor: pointer; }
button.copy:active { opacity: .7; }
button.copy svg { width: 18px; height: 18px; fill: none; stroke: currentColor; stroke-width: 2; stroke-linecap: round; stroke-linejoin: round; }
.copied { color: var(--ok); font-size: 13px; min-height: 1.4em; margin: 4px 0 8px; }
ol.steps { text-align: left; color: var(--muted); font-size: 14px; margin: 4px 0 20px; padding-left: 20px; }
ol.steps li { margin: 0 0 4px; }
input[type=password]:focus { outline: 2px solid var(--accent); outline-offset: -1px; }
.error { color: var(--accent); font-size: 15px; margin: 0 0 14px; }
code { font: 13px ui-monospace, SFMono-Regular, Menlo, monospace; background: var(--bg); padding: 2px 6px; border-radius: 6px; white-space: nowrap; }
.list { text-align: left; padding: 22px 0 8px; }
.list h3 { font-size: 13px; font-weight: 600; color: var(--muted); text-transform: uppercase; letter-spacing: .04em; margin: 0 24px 8px; }
.row { display: flex; align-items: center; gap: 12px; padding: 12px 24px; border-top: 1px solid var(--line); }
.row:first-of-type { border-top: 0; }
.row .what { flex: 1; min-width: 0; }
.row .name { font-weight: 600; }
.row .meta { color: var(--muted); font-size: 13px; }
.link { background: none; border: 0; padding: 6px 0; font: inherit; font-size: 15px; color: var(--accent); cursor: pointer; }
.empty { color: var(--muted); font-size: 15px; padding: 4px 24px 14px; margin: 0; }
.addresses { margin: 20px -8px 0; text-align: left; }
.addresses h3 { font-size: 13px; font-weight: 600; color: var(--muted); text-transform: uppercase; letter-spacing: .04em; margin: 0 8px 8px; }
.chip { display: flex; justify-content: space-between; gap: 10px; padding: 10px 12px; margin: 0 0 6px; border-radius: 12px;
  border: 1px solid var(--line); color: var(--text); text-decoration: none; font-size: 15px; word-break: break-all; }
.chip span { color: var(--muted); font-size: 13px; white-space: nowrap; }
.chip.on { border-color: var(--accent); box-shadow: inset 0 0 0 1px var(--accent); }
.addresses .hint { margin: 8px 8px 0; }
footer { text-align: center; color: var(--muted); font-size: 12px; margin-top: 8px; }
[hidden] { display: none !important; }
</style>
`;

const header = `<header>
<svg viewBox="0 0 24 24" aria-hidden="true"><defs><linearGradient id="g" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#ff5e5b"/><stop offset="1" stop-color="#ff2d55"/></linearGradient></defs>
<path fill="url(#g)" d="M12 21.2C12 21.2 2.5 15.4 2.5 8.9 2.5 6 4.8 3.8 7.6 3.8c1.9 0 3.5 1 4.4 2.6.9-1.6 2.5-2.6 4.4-2.6 2.8 0 5.1 2.2 5.1 5.1 0 6.5-9.5 12.3-9.5 12.3z"/></svg>
<h1>Health Relay</h1>
</header>`;

const copyButton = (id) => `<button class="copy" type="button" data-copy="${id}" aria-label="Copy" title="Copy"><svg viewBox="0 0 24 24" aria-hidden="true"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1"/></svg></button>`;

const copyScript = `// Copy buttons: the Clipboard API where the page is a secure context (HTTPS, localhost);
// elsewhere (plain HTTP on a LAN address) the text is selected for Ctrl+C or Cmd+C.
document.querySelectorAll("button.copy").forEach(function (b) {
  b.addEventListener("click", function () {
    var el = document.getElementById(b.getAttribute("data-copy")), status = b.parentNode.nextElementSibling;
    var say = function (msg) { status.textContent = msg; setTimeout(function () { status.textContent = ""; }, 3000); };
    var select = function () {
      var r = document.createRange(); r.selectNodeContents(el);
      var sel = window.getSelection(); sel.removeAllRanges(); sel.addRange(r);
    };
    var fallback = function () {
      select();
      var ok = false;
      try { ok = document.execCommand("copy"); } catch (e) {}
      say(ok ? "Copied" : "Selected: press Ctrl+C (Cmd+C on a Mac) to copy");
    };
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(el.textContent).then(function () { say("Copied"); }, fallback);
    } else {
      fallback();
    }
  });
});`;

function render(status, version, body) {
  const nonce = [...crypto.getRandomValues(new Uint8Array(16))].map((b) => b.toString(16).padStart(2, "0")).join("");
  const html = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="robots" content="noindex">
<meta name="color-scheme" content="light dark">
<title>Health Relay · Setup</title>${style}
</head>
<body>
<main>
${header}
${body(nonce)}
<script nonce="${nonce}">
${copyScript}
</script>
<footer>healthkit-relay ${esc(version)}</footer>
</main>
</body>
</html>
`;
  return new Response(html, {
    status,
    headers: {
      "Content-Type": "text/html; charset=utf-8",
      "Cache-Control": "no-store",
      "Referrer-Policy": "no-referrer",
      "X-Content-Type-Options": "nosniff",
      "X-Frame-Options": "DENY",
      "Content-Security-Policy": `default-src 'none'; style-src 'unsafe-inline'; img-src data:; script-src 'nonce-${nonce}'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'`,
    },
  });
}

export function loginPage(status, version, message, loginErr) {
  return render(status, version, (nonce) => `<section class="card">
  <h2>Sign in</h2>
  <p class="sub">Enter this server's admin token. Then you can pair an iPhone and create read tokens.</p>
  ${message ? `<p class="error">${esc(message)}</p>` : ""}
  <form class="login" method="post" action="/setup">
    <label for="token">Admin token</label>
    <input id="token" name="token" type="password" autocomplete="current-password" required autofocus>
    <button class="button" type="submit">Sign in</button>
  </form>
  <p class="hint">The server printed the admin token once, in its log, when it first started. To see it, run <code>docker compose logs relay</code> (Docker) or <code>journalctl -u healthkit-relay</code> (Linux service). Lost it? Run <code>healthkit-relay admin reset</code> on the server for a new one.</p>
  <p class="hint">To pair an iPhone without signing in, run <code>healthkit-relay pair</code> on the server: it shows the QR code in the terminal.</p>
</section>
<script nonce="${nonce}">
// Keeps #read-tokens (from /docs) across signing in.
if (location.hash) document.querySelector("form.login").action = "/setup" + location.hash;
</script>`);
}

// pairPage is the demo variant of the pairing page: the code pairs any number of iPhones.
export function pairPage(version, server, code) {
  const link = pairLink(server, code);
  const setupLink = server + "/setup?code=" + code;
  const qr = qrSVG(encodeQR(new TextEncoder().encode(link)));
  const host = server.replace(/^https?:\/\//, "");
  return render(200, version, (nonce) => `<section class="card" id="pair">
  <h2>Pair your iPhone</h2>
  <div class="desktop">
    <p class="sub">On the iPhone, with Health Relay installed, point the Camera at this QR code. Then tap <b>Open in Health Relay</b>.</p>
    <div class="qr">${qr}</div>
    <p class="hint">Or send this link to the iPhone and open it there:</p>
    <div class="secret"><code class="token" id="setup-link">${esc(setupLink)}</code>${copyButton("setup-link")}</div>
    <p class="copied" role="status"></p>
  </div>
  <div class="phone">
    <p class="sub">Pairs Health Relay on this iPhone with the server at ${esc(host)}.</p>
    <a class="button" href="${esc(link)}">Open in Health Relay</a>
  </div>
  <p class="hint">Or, in Health Relay, tap <b>Enter Server Details Instead</b> and enter the server address <code>${esc(server)}</code> and the setup code <code>${esc(code)}</code>.</p>
  <p class="status" id="status"><span class="dot"></span><span id="status-text">This is a demo server: its setup code pairs any number of iPhones, and it erases all uploaded data every hour.</span></p>
</section>
<section class="card" id="done" hidden>
  <svg class="check" viewBox="0 0 64 64" aria-hidden="true"><circle cx="32" cy="32" r="30" fill="#34c759"/><path d="M19 33l9 9 17-19" fill="none" stroke="#fff" stroke-width="5" stroke-linecap="round" stroke-linejoin="round"/></svg>
  <h2>iPhone paired</h2>
  <p class="sub" id="done-text">On the iPhone, Health now asks what Health Relay may read: allow all of it. Health Relay then syncs on its own. You can close this page.</p>
</section>
<script nonce="${nonce}">
(function () {
  var code = ${jsString(code)}, admin = false;
  var timer = setInterval(function () {
    fetch("/setup/status?code=" + encodeURIComponent(code), {cache: "no-store"}).then(function (r) { return r.json(); }).then(function (s) {
      if (s.state === "paired") {
        clearInterval(timer);
        document.getElementById("pair").hidden = true;
        var done = document.getElementById("done");
        done.hidden = false;
        if (s.device) document.getElementById("done-text").textContent = s.device + (s.model ? " (" + s.model + ")" : "") +
          " is paired. On the iPhone, Health now asks what Health Relay may read: allow all of it. Health Relay then syncs on its own. You can close this page.";
      } else if (s.state === "invalid") {
        clearInterval(timer);
        document.getElementById("status-text").textContent = "This setup code expired. For a new one, sign in to the setup page or run healthkit-relay pair on the server.";
      }
    }).catch(function () {});
  }, 2000);
})();
</script>`);
}

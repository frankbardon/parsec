// parsec admin — tiny vanilla JS helper. No bundler, no deps.
// Stores the operator's mgmt bearer in sessionStorage (cleared on tab close).

const TOKEN_KEY = "parsec.mgmt.token";

export function getToken() {
  return sessionStorage.getItem(TOKEN_KEY) || "";
}
export function setToken(t) {
  if (t) sessionStorage.setItem(TOKEN_KEY, t);
  else sessionStorage.removeItem(TOKEN_KEY);
}
export function clearToken() {
  sessionStorage.removeItem(TOKEN_KEY);
}

// Guard every page except login.html. Redirects to login when no token.
export function requireToken() {
  if (!getToken()) {
    window.location.href = "login.html";
    return false;
  }
  return true;
}

// parsecCall posts a JSON body to /twirp/parsec.ParsecService/<method> with
// the operator bearer attached. On 401 redirects to login. Other non-2xx
// throw an Error whose .message is the server's error text.
export async function parsecCall(method, body) {
  const token = getToken();
  const res = await fetch("/twirp/parsec.ParsecService/" + method, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "Authorization": "Bearer " + token,
    },
    body: JSON.stringify(body || {}),
  });
  if (res.status === 401) {
    clearToken();
    window.location.href = "login.html";
    throw new Error("unauthorized");
  }
  const text = await res.text();
  let parsed = null;
  try { parsed = text ? JSON.parse(text) : {}; } catch (_) { parsed = { msg: text }; }
  if (!res.ok) {
    const msg = (parsed && (parsed.msg || parsed.message)) || res.statusText;
    const err = new Error(msg);
    err.code = parsed && parsed.code;
    throw err;
  }
  return parsed;
}

export function showError(el, err) {
  if (!el) return;
  el.textContent = err && err.message ? err.message : String(err);
}

// Base64 helpers — DLQ items carry recipient as base64 JSON bytes (twirp
// JSON encodes proto `bytes` fields as base64).
export function b64decode(s) {
  if (!s) return "";
  try { return atob(s); } catch (_) { return s; }
}

export function signOut() {
  clearToken();
  window.location.href = "login.html";
}

// Install nav sign-out behaviour on every page.
export function mountNav() {
  const out = document.querySelector(".signout");
  if (out) out.addEventListener("click", (e) => { e.preventDefault(); signOut(); });
}

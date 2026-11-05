// DOM helpers shared by the checkout and success pages. Text always goes in
// with textContent, never innerHTML, so server data can't inject HTML.
// The logic worth testing lives in lib.js. This file only draws.
import * as lib from "./lib.js";

export const $ = (id) => document.getElementById(id);

export async function fetchJSON(url, init) {
  const res = await fetch(url, init);
  const body = await res.json().catch(() => null);
  if (!res.ok) throw new Error(body?.error || `HTTP ${res.status}`);
  return body;
}

// setSegments builds text with bold parts from lib.js segments.
export function setSegments(el, segments) {
  el.replaceChildren(...segments.map((s) => {
    if (!s.strong) return document.createTextNode(s.text);
    const b = document.createElement("strong");
    b.textContent = s.text;
    return b;
  }));
}

// renderSummary fills the "Your order" aside: lines, total, the weekly line.
export function renderSummary(order) {
  const rows = order.items.map((it) => {
    const li = document.createElement("li");
    const name = document.createElement("span");
    name.textContent = it.name;
    if (it.add_on) {
      const tag = document.createElement("span");
      tag.className = "tag";
      tag.textContent = "Add-on";
      name.append(tag);
    }
    const price = document.createElement("span");
    price.textContent = lib.formatAmount(lib.lineTotal(it), order.currency);
    li.append(name, price);
    return li;
  });
  const delivery = document.createElement("li");
  delivery.innerHTML = "<span>Delivery</span><span>Free</span>"; // fixed text, no data
  $("lines").replaceChildren(...rows, delivery);
  $("total").textContent = lib.formatAmount(order.amount, order.currency);
  const next = lib.summaryNext(order);
  $("summary-next").hidden = next === "";
  $("summary-next").textContent = next;
}

// syncDemoStrip shows a valid basket and country. Subscriptions are FR only,
// so the country select is off for them.
export function syncDemoStrip(basket, country) {
  const sel = lib.demoSelection(basket, country);
  const allowed = lib.countriesFor(sel.basket);
  $("demo-basket").value = sel.basket;
  for (const opt of $("demo-country").options) opt.disabled = !allowed.includes(opt.value);
  $("demo-country").value = sel.country;
  $("demo-country").disabled = allowed.length < 2;
  return sel;
}

// onDemoChange calls handler with the new, valid basket and country.
export function onDemoChange(handler) {
  const fire = () => handler(syncDemoStrip($("demo-basket").value, $("demo-country").value));
  $("demo-basket").addEventListener("change", fire);
  $("demo-country").addEventListener("change", fire);
}

// showBanner fills the error banner: { headline, detail, help, attempt }.
// Empty parts are hidden.
export function showBanner({ headline, detail = "", help = "", attempt = "" }) {
  $("error-headline").textContent = headline;
  $("error-detail").textContent = detail ? ` ${detail}` : "";
  $("error-help").textContent = help;
  $("error-help").hidden = help === "";
  $("error-attempt").textContent = attempt;
  $("error-attempt").hidden = attempt === "";
  $("error-banner").hidden = false;
}

export function hideBanner() {
  $("error-banner").hidden = true;
}

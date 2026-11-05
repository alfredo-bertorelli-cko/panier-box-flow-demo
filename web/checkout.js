// Checkout page: creates the order and session, renders the summary, mounts Flow.
// All text and amounts come from the server through lib.js. This file only
// touches the DOM, so the logic worth testing lives in lib.js.
import * as lib from "./lib.js";

const $ = (id) => document.getElementById(id);

let flowComponent = null;
let configPromise = null;
// Each load gets a number. A slow response from an earlier load (say, a quick
// basket switch) is ignored instead of overwriting the newer one.
let currentLoad = 0;

async function fetchJSON(url, init) {
  const res = await fetch(url, init);
  const body = await res.json().catch(() => null);
  if (!res.ok) throw new Error(body?.error || `HTTP ${res.status}`);
  return body;
}

// The config never changes while the page is open, so fetch it once.
function getConfig() {
  configPromise ??= fetchJSON("/api/config");
  return configPromise;
}

// setSegments builds text with bold parts using textContent only.
function setSegments(el, segments) {
  el.replaceChildren(...segments.map((s) => {
    if (!s.strong) return document.createTextNode(s.text);
    const b = document.createElement("strong");
    b.textContent = s.text;
    return b;
  }));
}

function renderLines(order) {
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
}

function renderOrder(order) {
  setSegments($("reserve-text"), lib.bannerSegments(order));

  $("customer-name").textContent = order.customer.name;
  $("customer-address").textContent = lib.addressLine(order.customer.address);
  $("customer-email").textContent = order.customer.email;

  const terms = lib.termsSegments(order);
  $("terms").hidden = terms === null;
  if (terms) setSegments($("terms-text"), terms);

  $("legal-weekly").textContent = lib.legalWeekly(order);

  renderLines(order);
  $("total").textContent = lib.formatAmount(order.amount, order.currency);
  const next = lib.summaryNext(order);
  $("summary-next").hidden = next === "";
  $("summary-next").textContent = next;
}

function showError(message) {
  $("error-text").textContent = message;
  $("error-banner").hidden = false;
}

function hideError() {
  $("error-banner").hidden = true;
}

// load makes a new order and a new session every time (F11), then mounts Flow.
async function load(basket, country) {
  const thisLoad = ++currentLoad;
  hideError();
  if (flowComponent) {
    flowComponent.unmount();
    flowComponent = null;
  }
  $("flow-container").replaceChildren();

  try {
    const config = await getConfig();
    const body = await fetchJSON("/api/payment-sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ basket, country }), // never an amount: the server owns it
    });
    if (thisLoad !== currentLoad) return;
    const { order, paymentSession } = lib.readSessionResponse(body);
    renderOrder(order);

    // CheckoutWebComponents is the global from Checkout.com's script tag.
    const checkout = await CheckoutWebComponents(lib.flowOptions(config, paymentSession, {
      // Payments without a redirect end here (F2). Redirects come back to the
      // same success page through success_url (F3).
      onPaymentCompleted: (_self, paymentResponse) => {
        window.location.assign(lib.successUrl(paymentResponse.id));
      },
      // A decline or a Flow problem. Flow keeps the form, so Marie can try again.
      // Log the code only, never the whole error object.
      onError: (_component, error) => {
        console.warn("Flow error:", error?.code ?? error?.type ?? "unknown");
        showError(lib.PAYMENT_ERROR_MESSAGE);
      },
    }));
    if (thisLoad !== currentLoad) return;
    flowComponent = checkout.create("flow");
    flowComponent.mount($("flow-container"));
  } catch (err) {
    if (thisLoad !== currentLoad) return;
    console.warn("Checkout failed to load:", err.message);
    showError(lib.LOAD_ERROR_MESSAGE);
  }
}

// Demo strip: any change is a new order and session. Flow's update() can't
// change payment type, country or methods (D-009). Country only applies to
// the one-off box.
function syncDemoStrip(basket, country) {
  const sel = lib.demoSelection(basket, country);
  const allowed = lib.countriesFor(sel.basket);
  $("demo-basket").value = sel.basket;
  for (const opt of $("demo-country").options) opt.disabled = !allowed.includes(opt.value);
  $("demo-country").value = sel.country;
  $("demo-country").disabled = allowed.length < 2;
  return sel;
}

function onDemoChange() {
  const sel = syncDemoStrip($("demo-basket").value, $("demo-country").value);
  load(sel.basket, sel.country);
}

$("demo-basket").addEventListener("change", onDemoChange);
$("demo-country").addEventListener("change", onDemoChange);
// Anchors in the legal line are placeholders: keep the page where it is.
for (const a of document.querySelectorAll(".legal a")) a.addEventListener("click", (e) => e.preventDefault());

const start = syncDemoStrip("subscription", "FR");
load(start.basket, start.country);

// Checkout page: creates the order and session, renders the summary, mounts Flow.
// All text and amounts come from the server through lib.js. This file only
// touches the DOM, so the logic worth testing lives in lib.js.
import * as lib from "./lib.js";
import { $, fetchJSON, setSegments, renderSummary, syncDemoStrip, onDemoChange, showBanner, hideBanner } from "./page.js";
import { appearance } from "./theme.js";

let flowComponent = null;
let configPromise = null;
// Each load gets a number. A slow response from an earlier load (say, a quick
// basket switch) is ignored instead of overwriting the newer one.
let currentLoad = 0;

// The config never changes while the page is open, so fetch it once.
function getConfig() {
  configPromise ??= fetchJSON("/api/config");
  return configPromise;
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
  renderSummary(order);
}

// load makes a new order and a new session every time (F11), then mounts Flow.
// It hides the banner first, synchronously, so a caller can show one right after.
async function load(basket, country) {
  const thisLoad = ++currentLoad;
  hideBanner();
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
      // A decline or a Flow problem. Flow keeps the form, so Marie can try
      // again on the same order. Log the code only, never the whole error object.
      onError: (_component, error) => {
        console.warn("Flow error:", error?.code ?? error?.type ?? "unknown");
        showBanner(lib.failureBanner(null));
      },
    }, appearance));
    if (thisLoad !== currentLoad) return;
    flowComponent = checkout.create("flow");
    flowComponent.mount($("flow-container"));
  } catch (err) {
    if (thisLoad !== currentLoad) return;
    console.warn("Checkout failed to load:", err.message);
    showBanner({ headline: lib.LOAD_ERROR_MESSAGE });
  }
}

// failedAttempt looks up the payment Checkout.com sent back to failure_url
// (F3), so the banner can say why. Our GET also stores it on the old order.
// If the lookup fails, the banner still shows, just without the detail.
async function failedAttempt(paymentId) {
  if (!paymentId) return { payment: null, order: null };
  try {
    return lib.readPaymentResponse(await fetchJSON(lib.paymentApiUrl(paymentId)));
  } catch (err) {
    console.warn("Failed payment lookup:", err.message);
    return { payment: null, order: null };
  }
}

async function start() {
  // Anchors in the legal line are placeholders: keep the page where it is.
  for (const a of document.querySelectorAll(".legal a")) a.addEventListener("click", (e) => e.preventDefault());
  // Demo strip: any change is a new order and session. Flow's update() can't
  // change payment type, country or methods (D-009).
  onDemoChange((sel) => load(sel.basket, sel.country));

  const params = lib.paymentParams(window.location.search);
  let choice = lib.startChoice(window.location.search);
  let banner = null;
  if (params.failed) {
    const { payment, order } = await failedAttempt(params.paymentId);
    // Retry on the same basket the failed order had.
    if (order) choice = lib.demoSelection(order.basket, order.country);
    banner = lib.failureBanner(payment, { order });
  }
  // Drop the query, so a reload starts clean (F11) instead of showing the
  // old failure again.
  window.history.replaceState(null, "", "/");

  const sel = syncDemoStrip(choice.basket, choice.country);
  load(sel.basket, sel.country);
  if (banner) showBanner(banner);
}

start();

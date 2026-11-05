// Success page (F4). Reads cko-payment-id from the URL, asks our server for
// the payment, and shows the order. Our server GETs it from Checkout.com and
// stores it, so the status here is never taken from the URL or the browser.
import * as lib from "./lib.js";
import { $, fetchJSON, setSegments, renderSummary, syncDemoStrip, onDemoChange, showBanner } from "./page.js";

function renderFacts(facts) {
  $("facts").replaceChildren(...facts.map((f) => {
    const box = document.createElement("div");
    box.className = "fact";
    const label = document.createElement("span");
    label.className = "label";
    label.textContent = f.label;
    const value = document.createElement("span");
    value.className = "value";
    value.textContent = f.value;
    box.append(label, value);
    return box;
  }));
}

function renderPaid(order, payment) {
  $("title").textContent = lib.successTitle(order);
  document.title = `Panier Box | Order ${order.id} confirmed`;
  setSegments($("hero-text"), lib.heroSegments(order, payment));
  renderFacts(lib.successFacts(order, payment));
  $("manage").hidden = !lib.isRecurring(order);
  $("plan").textContent = lib.planTag(order);
  renderSummary(order);
  $("confirmed").hidden = false;
  $("summary").hidden = false;
}

async function show() {
  const { paymentId } = lib.paymentParams(window.location.search);
  $("pending").hidden = true;
  try {
    if (!paymentId) throw new Error("no cko-payment-id in the URL");
    const { order, payment } = lib.readPaymentResponse(await fetchJSON(lib.paymentApiUrl(paymentId)));
    syncDemoStrip(order.basket, order.country);

    switch (lib.pageState(payment.status)) {
      case "failed":
        // One failure UI: the checkout page's banner, with a fresh order (F4).
        window.location.replace(lib.failureUrl(payment.payment_id));
        return;
      case "pending":
        $("pending").hidden = false;
        return;
      default:
        renderPaid(order, payment);
    }
  } catch (err) {
    console.warn("Order failed to load:", err.message);
    showBanner({ headline: lib.ORDER_LOAD_ERROR_MESSAGE });
  }
}

// The strip shows this order. A change opens checkout with that choice, which
// creates a new order and session there. Nothing changes on this page.
onDemoChange((sel) => window.location.assign(lib.checkoutUrl(sel.basket, sel.country)));
$("refresh").addEventListener("click", show);
show();

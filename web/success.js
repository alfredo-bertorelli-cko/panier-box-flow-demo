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
  $("title-check").hidden = false;
  document.title = `Panier Box | Order ${order.id} confirmed`;
  setSegments($("hero-text"), lib.heroSegments(order, payment));
  renderFacts(lib.successFacts(order, payment));
  $("manage").hidden = !lib.isRecurring(order);
  $("plan").textContent = lib.planTag(order);
  renderSummary(order);
  $("confirmed").hidden = false;
  $("summary").hidden = false;
}

// el builds an element with a class and text. textContent only.
function el(tag, className, text) {
  const e = document.createElement(tag);
  if (className) e.className = className;
  if (text !== undefined) e.textContent = text;
  return e;
}

function renderOrderBar(order) {
  $("order-bar").replaceChildren(...lib.orderBar(order).map((f) => {
    const span = el("span", "", `${f.label} `);
    span.append(el("b", f.mono ? "mono" : "", f.value));
    return span;
  }));
}

// payCard draws one status card, laid out like the mockup's article.pay-card.
function payCard(order, payment, newest) {
  const card = el("article", newest ? "pay-card newest" : "pay-card");
  card.dataset.paymentId = payment.payment_id;

  const top = el("div", "pc-top");
  top.append(
    el("span", "pc-amount", lib.formatAmount(payment.amount, order.currency)),
    el("span", "kind", payment.kind || "?"),
    el("span", `status ${lib.statusClass(payment.status)}`, payment.status),
  );

  const ids = el("div", "pc-ids");
  ids.append(
    el("span", "", lib.cardLabel(order, payment)),
    el("span", "mono", payment.payment_id),
    el("span", "mono", payment.reference),
  );

  const m = lib.methodLine(payment);
  const method = el("div", "pc-method");
  if (m.badge) method.append(el("span", "scheme", m.badge));
  method.append(document.createTextNode(` ${m.text}`));
  if (m.wallet) method.append(el("span", "wallet", m.wallet));

  const balances = el("div", "balances");
  balances.append(...lib.balanceCells(payment, order.currency).map((c) => {
    const cell = el("div", c.zero ? "zero" : "");
    cell.append(el("span", "", c.label), el("b", "", c.value));
    return cell;
  }));

  const details = el("details");
  const dl = el("dl");
  for (const row of lib.paymentDetails(order, payment)) dl.append(el("dt", "", row.term), el("dd", row.mono ? "mono" : "", row.value));
  details.append(el("summary", "", "Details"), dl);
  const foot = el("div", "pc-foot");
  foot.append(details);
  const buttons = lib.cardButtons(payment);
  if (buttons.length) {
    const group = el("div", "pc-buttons");
    for (const b of buttons) {
      const btn = el("button", "btn secondary small auto", b.label);
      btn.type = "button";
      btn.disabled = busy.has(payment.payment_id);
      btn.addEventListener("click", () => cardAction(b.action, payment.payment_id, payment.status));
      group.append(btn);
    }
    foot.append(group);
  }

  card.append(top, ids, method, balances, foot);
  const note = cardNotes.get(payment.payment_id);
  if (note) card.append(el("p", "reason", note));
  return card;
}

// Per card, until reload: payments with an action in flight (all their
// buttons off, so Capture and Reverse can't race), and the last message (an
// error, or "still processing").
const busy = new Set();
const cardNotes = new Map();

// cardAction runs a card's button: "capture" (step 4.4b) or "reverse" (F7:
// the server voids or refunds from that payment's balances). Both are async
// at Checkout.com, so poll that payment until its status changes. The card
// stays where it is on screen throughout.
async function cardAction(action, paymentId, before) {
  pinnedPaymentId = paymentId;
  busy.add(paymentId);
  cardNotes.delete(paymentId);
  keepPinnedCard(() => renderBackOffice(currentOrder));
  try {
    const body = await fetchJSON(`/api/payments/${encodeURIComponent(paymentId)}/${action}`, { method: "POST" });
    const { order, payment } = lib.readPaymentResponse(body);
    renderAll(order, payment);
    if (payment.status === before && !(await pollPayment(paymentId, before))) {
      cardNotes.set(paymentId, lib.STILL_PROCESSING_MESSAGE);
    }
  } catch (err) {
    console.warn(`Card ${action} failed:`, err.message);
    cardNotes.set(paymentId, lib.actionError(err));
  } finally {
    busy.delete(paymentId);
    keepPinnedCard(() => renderBackOffice(currentOrder));
  }
}

// Set after a successful Dispatch click, until the page reloads.
let captureSentAt = null;

function renderDispatch(order, error = "") {
  const a = lib.dispatchAction(order, { sentAt: captureSentAt });
  $("dispatch-title").textContent = a.title;
  $("dispatch").textContent = a.title;
  $("dispatch-what").textContent = a.what;
  $("dispatch").disabled = !a.enabled;
  const reason = error || a.reason;
  $("dispatch-reason").textContent = reason;
  $("dispatch-reason").hidden = reason === "";
}

let currentOrder = null;
// The payment this page was opened for (cko-payment-id): the hero always
// describes it, even after a renewal adds a newer payment to the order.
let pagePaymentId = "";

// The card being acted on (a Reverse click). Redraws keep it where it is on
// screen, so the demo shows its status and balances change in place.
let pinnedPaymentId = "";

function cardTop(paymentId) {
  if (!paymentId) return undefined;
  const card = document.querySelector(`article[data-payment-id="${CSS.escape(paymentId)}"]`);
  return card?.getBoundingClientRect().top;
}

// keepPinnedCard runs a redraw. Every redraw replaces all the cards, which
// loses the browser's place on the page. So measure the pinned card before
// and after, and scroll by the difference.
function keepPinnedCard(draw) {
  const before = cardTop(pinnedPaymentId);
  draw();
  const after = cardTop(pinnedPaymentId);
  if (before !== undefined && after !== undefined) window.scrollBy(0, after - before);
}

function renderAll(order, payment) {
  keepPinnedCard(() => {
    const hero = order.payments.find((p) => p.payment_id === pagePaymentId) ?? payment;
    renderPaid(order, hero);
    renderBackOffice(order);
  });
}

// The renewal just run from this page: { week, payment }, until reload.
let lastRenewal = null;

function renderRenew(order, error = "") {
  const a = lib.renewAction(order, lastRenewal);
  $("renew-what").textContent = a.what;
  $("renew").disabled = !a.enabled;
  const reason = error || a.reason;
  $("renew-reason").textContent = reason;
  $("renew-reason").hidden = reason === "";
}

// renew runs next week's MIT (F6). The server builds it from the stored card
// and the first payment. The browser only names the order. An MIT has no
// customer and no 3DS, so it's normally Authorized or Declined at once. If
// it's Pending, poll like Dispatch.
async function renew() {
  pinnedPaymentId = ""; // the new renewal card appears on top: let it
  $("renew").disabled = true;
  const week = currentOrder.next_renewal_week;
  try {
    const body = await fetchJSON(`/api/orders/${encodeURIComponent(currentOrder.id)}/renewals`, { method: "POST" });
    const { order, payment } = lib.readPaymentResponse(body);
    lastRenewal = { week, payment };
    renderAll(order, payment);
    if (payment.status === "Pending" && !(await pollPayment(payment.payment_id, payment.status))) {
      renderRenew(currentOrder, lib.STILL_PROCESSING_MESSAGE);
    }
  } catch (err) {
    console.warn("Renewal failed:", err.message);
    renderRenew(currentOrder, lib.actionError(err));
  }
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
let polling = false;

// pollPayment re-reads one payment every second until its status is no
// longer `before`, at most 10 times (F5), redrawing after each read so the
// balances update as they land. Returns false if it never changed.
async function pollPayment(paymentId, before) {
  if (polling) return true; // one poll at a time
  polling = true;
  try {
    const { done } = await lib.pollUntil({
      fetchOnce: async () => {
        const body = await fetchJSON(lib.paymentApiUrl(paymentId));
        const { order, payment } = lib.readPaymentResponse(body);
        if (lastRenewal?.payment.payment_id === payment.payment_id) lastRenewal.payment = payment;
        renderAll(order, payment);
        return body;
      },
      isDone: lib.statusChangedFrom(before),
      sleep,
    });
    return done;
  } finally {
    polling = false;
  }
}

// dispatch captures the first payment (F5). The server picks the payment
// and the amount. The browser only names the order. The capture is
// asynchronous, so the payment is then polled until it leaves Authorized.
async function dispatch() {
  pinnedPaymentId = ""; // the panels are at the top: nothing to hold in place
  $("dispatch").disabled = true;
  try {
    const body = await fetchJSON(`/api/orders/${encodeURIComponent(currentOrder.id)}/capture`, { method: "POST" });
    const { order, payment } = lib.readPaymentResponse(body);
    captureSentAt = new Date();
    renderAll(order, payment);
    if (payment.status === "Authorized" && !(await pollPayment(payment.payment_id, payment.status))) {
      renderDispatch(currentOrder, lib.STILL_PROCESSING_MESSAGE);
    }
  } catch (err) {
    console.warn("Dispatch failed:", err.message);
    renderDispatch(currentOrder, lib.actionError(err));
  }
}

function renderBackOffice(order) {
  currentOrder = order;
  renderDispatch(order);
  renderRenew(order);
  renderOrderBar(order);
  const cards = lib.paymentsNewestFirst(order).map((p, i) => payCard(order, p, i === 0));
  $("pay-cards").querySelectorAll("article").forEach((a) => a.remove());
  $("pay-cards").append(...cards);
  $("backoffice").hidden = false;
}

// Live log (D-041): one EventSource for this order, opened once. The browser
// reconnects by itself when it drops, and the server then resends the
// backlog, so the list is cleared on every (re)open to avoid doubles.
let stream = null;

function setLogStatus(connected) {
  const b = lib.liveBadge(connected);
  $("log-status").className = b.cls;
  $("log-status").textContent = b.text;
}

function appendLogLine(entry) {
  const line = lib.logLine(entry);
  const text = el("span");
  line.parts.forEach((p, i) => {
    if (i) text.append(" ");
    text.append(p.cls ? el("span", p.cls, p.text) : document.createTextNode(p.text));
  });
  const time = el("time", "", line.time);
  time.dateTime = entry.time ?? "";
  const li = el("li", line.lineCls);
  li.append(time, el("span", "dir", line.dir), text);

  // Newest at the top (Alfredo, 5.4b). The backlog arrives oldest first, so
  // prepending each line gives newest first too. Stay at the top unless ops
  // has scrolled down to read older lines.
  const list = $("log-lines");
  const atTop = list.scrollTop < 24;
  list.prepend(li);
  while (list.children.length > lib.LOG_LIMIT) list.lastElementChild.remove();
  if (atTop) list.scrollTop = 0;
}

function startLog(orderId) {
  if (stream) return;
  stream = new EventSource(lib.eventsUrl(orderId));
  stream.addEventListener("open", () => {
    $("log-lines").replaceChildren();
    setLogStatus(true);
  });
  stream.addEventListener("error", () => setLogStatus(false));
  stream.addEventListener("message", (ev) => {
    let entry;
    try {
      entry = JSON.parse(ev.data);
    } catch {
      return;
    }
    appendLogLine(entry);
    if (lib.refreshesOrder(entry)) scheduleRefresh();
  });
}

// A read line means a stored status changed, often from a webhook's GET.
// Re-read the order from our own store (no Checkout.com call) and redraw.
// Lines come in bursts (the backlog, a webhook then its read), so wait a
// moment and refresh once.
let refreshTimer = null;

function scheduleRefresh() {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(refreshOrder, 300);
}

async function refreshOrder() {
  if (!currentOrder) {
    // Still on the pending banner: a webhook may have confirmed the payment.
    if (!$("pending").hidden) show();
    return;
  }
  try {
    const { order } = await fetchJSON(`/api/orders/${encodeURIComponent(currentOrder.id)}`);
    const hero = order.payments.find((p) => p.payment_id === pagePaymentId) ?? order.payments.at(-1);
    if (!hero) return;
    const renewed = lastRenewal && order.payments.find((p) => p.payment_id === lastRenewal.payment.payment_id);
    if (renewed) lastRenewal.payment = renewed;
    renderAll(order, hero);
  } catch (err) {
    console.warn("Order refresh failed:", err.message);
  }
}

async function show() {
  const { paymentId } = lib.paymentParams(window.location.search);
  pagePaymentId = paymentId;
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
        startLog(order.id);
        return;
      default:
        renderPaid(order, payment);
        renderBackOffice(order);
        startLog(order.id);
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
$("dispatch").addEventListener("click", dispatch);
$("renew").addEventListener("click", renew);
show();

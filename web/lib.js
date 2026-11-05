// Pure helpers for the Panier Box pages. No DOM and no network here, so every
// function runs in Node and is tested with `node --test web/`.
// Every amount comes from the server in minor units (5000 = EUR 50.00).

const LOCALE = "en-GB";

// formatAmount(5000, "EUR") gives "€50.00".
export function formatAmount(minor, currency) {
  return new Intl.NumberFormat(LOCALE, { style: "currency", currency }).format(minor / 100);
}

// countryName("FR") gives "France", for the Delivery panel.
export function countryName(code) {
  return new Intl.DisplayNames([LOCALE], { type: "region" }).of(code);
}

export function lineTotal(item) {
  return item.unit_price * item.quantity;
}

// addOnTotal sums the one-off extras, for "including €20.00 of add-ons".
export function addOnTotal(items) {
  return items.filter((it) => it.add_on).reduce((sum, it) => sum + lineTotal(it), 0);
}

// isRecurring: only subscription orders carry a weekly renewal amount.
export function isRecurring(order) {
  return Number(order.renewal_amount) > 0;
}

// Text with bold parts is returned as segments, [{ text, strong }], so the page
// can build it with textContent. No innerHTML anywhere, so no HTML injection.

export function bannerSegments(order) {
  return [
    { text: "We reserve " },
    { text: formatAmount(order.amount, order.currency), strong: true },
    { text: " on your card today. You're only charged when your box ships." },
  ];
}

// termsSegments is the recurring terms block above Flow (PRD section 10). It is
// also dispute evidence for "cancelled recurring" claims (UC-15). One-off
// orders have no terms block: returns null.
export function termsSegments(order) {
  if (!isRecurring(order)) return null;
  const money = (minor) => formatAmount(minor, order.currency);
  const addOns = addOnTotal(order.items);
  const firstBox = addOns > 0
    ? ` today for your first box, including ${money(addOns)} of add-ons. Then `
    : " today for your first box. Then ";
  return [
    { text: "You pay " },
    { text: money(order.amount), strong: true },
    { text: firstBox },
    { text: `${money(order.renewal_amount)} every week`, strong: true },
    { text: " until you cancel. Your card is authorised when you order and charged when your box ships. " },
    { text: "Skip, pause or cancel any time.", strong: true },
  ];
}

// legalWeekly ends the legal line: the recurring part for subscriptions, or
// just the full stop for one-off orders.
export function legalWeekly(order) {
  if (!isRecurring(order)) return ".";
  return `, and to weekly payments of ${formatAmount(order.renewal_amount, order.currency)} until you cancel. ` +
    "You can cancel in your account up to 3 days before a delivery.";
}

// summaryNext is the line under the total, or "" for one-off orders.
// U+2011 is a non-breaking hyphen, so "one-off" never splits over two lines.
export function summaryNext(order) {
  if (!isRecurring(order)) return "";
  return `Then ${formatAmount(order.renewal_amount, order.currency)} a week for the box. Add-ons are one‑off.`;
}

// addressLine gives "12 rue Oberkampf, 75011 Paris, France".
export function addressLine(address) {
  return `${address.address_line1}, ${address.zip} ${address.city}, ${countryName(address.country)}`;
}

// readSessionResponse checks the shape of POST /api/payment-sessions before the
// page uses it. payment_session goes to Flow untouched.
export function readSessionResponse(body) {
  const order = body?.order;
  const paymentSession = body?.payment_session;
  if (!order?.id || !Array.isArray(order.items) || typeof order.amount !== "number" || !paymentSession?.id) {
    throw new Error("Unexpected response from the server.");
  }
  return { order, paymentSession };
}

// successUrl is where the browser goes after a payment without a redirect (F2).
// The same page also receives Checkout.com redirects with cko-payment-id (F3).
export function successUrl(paymentId) {
  return `/success?cko-payment-id=${encodeURIComponent(paymentId)}`;
}

// The demo strip: subscriptions are FR only, the one-off box FR or PT.
const COUNTRIES = { subscription: ["FR"], oneoff: ["FR", "PT"] };

export function countriesFor(basket) {
  return COUNTRIES[basket] ?? [];
}

// demoSelection keeps the strip valid: a country the basket doesn't allow
// falls back to the basket's first country.
export function demoSelection(basket, country) {
  const allowed = countriesFor(basket);
  if (allowed.length === 0) return { basket: "subscription", country: "FR" };
  return { basket, country: allowed.includes(country) ? country : allowed[0] };
}

// flowOptions builds the CheckoutWebComponents options. publicKey and
// environment come from /api/config, appearance from theme.js.
export function flowOptions(config, paymentSession, { onPaymentCompleted, onError }, appearance) {
  return {
    publicKey: config.publicKey,
    environment: config.environment,
    locale: LOCALE,
    paymentSession,
    appearance,
    onPaymentCompleted,
    onError,
  };
}

export const LOAD_ERROR_MESSAGE =
  "We couldn't load the payment form. Please refresh the page.";

export const ORDER_LOAD_ERROR_MESSAGE =
  "We couldn't load your order. Please refresh the page.";

// --- URLs -----------------------------------------------------------------

// paymentParams reads what Checkout.com and our own pages put in the URL:
// cko-payment-id on success_url and failure_url, payment=failed on failure_url.
export function paymentParams(search) {
  const p = new URLSearchParams(search);
  return { paymentId: p.get("cko-payment-id") ?? "", failed: p.get("payment") === "failed" };
}

// startChoice is the checkout page's first basket and country. The success
// page's demo strip links here with ?basket=&country= (step 3.3).
export function startChoice(search) {
  const p = new URLSearchParams(search);
  return demoSelection(p.get("basket") ?? "subscription", p.get("country") ?? "FR");
}

export function checkoutUrl(basket, country) {
  return `/?${new URLSearchParams({ basket, country })}`;
}

export function failureUrl(paymentId) {
  return `/?${new URLSearchParams({ payment: "failed", "cko-payment-id": paymentId })}`;
}

export function paymentApiUrl(paymentId) {
  return `/api/payments/${encodeURIComponent(paymentId)}`;
}

// readPaymentResponse checks the shape of GET /api/payments/{id}.
export function readPaymentResponse(body) {
  const order = body?.order;
  const payment = body?.payment;
  if (!order?.id || !Array.isArray(order.items) || !payment?.payment_id || !payment.status) {
    throw new Error("Unexpected response from the server.");
  }
  return { order, payment };
}

// --- Success page ---------------------------------------------------------

// pageState picks which success page to show from Checkout.com's status.
// Declined, Expired and Canceled never took money: Marie goes back to
// checkout with the failure banner (F4). Voided and Refunded were paid once,
// so the order page still shows (the ops panel reverses them in step 4).
const FAILED = ["Declined", "Expired", "Canceled"];

export function pageState(status) {
  if (status === "Pending") return "pending";
  if (FAILED.includes(status)) return "failed";
  return "paid";
}

export function firstName(name) {
  return String(name ?? "").trim().split(/\s+/)[0] || "there";
}

// processing.scheme first: on a co-badged card that's the network Marie chose
// (D-027). card_schemes is the card's own brands, for when nothing was processed.
const SCHEME_NAMES = { visa: "Visa", mastercard: "Mastercard", cartes_bancaires: "CB", amex: "Amex" };

export function schemeLabel(method) {
  const s = method?.scheme || method?.card_schemes?.[0] || "";
  return SCHEME_NAMES[s] ?? s;
}

const METHOD_NAMES = { mbway: "MB WAY", googlepay: "Google Pay" };

// methodLabel is the short form for the "Paid with" fact: "Visa ···· 4242",
// "Google Pay, Visa ···· 1387", "MB WAY".
export function methodLabel(payment) {
  const m = payment?.method ?? {};
  if (m.type !== "card") return METHOD_NAMES[m.type] ?? m.type ?? "";
  const card = [schemeLabel(m), m.last4 ? `···· ${m.last4}` : ""].filter(Boolean).join(" ") || "Card";
  return m.wallet === "googlepay" ? `Google Pay, ${card}` : card;
}

// cardPhrase is the long form for a sentence: "your Visa ending 4242",
// "your Visa ending 1387 in Google Pay", "MB WAY".
function cardPhrase(payment) {
  const m = payment?.method ?? {};
  if (m.type !== "card") return METHOD_NAMES[m.type] ?? "your payment method";
  const scheme = schemeLabel(m) || "card";
  const card = m.last4 ? `your ${scheme} ending ${m.last4}` : `your ${scheme}`;
  return m.wallet === "googlepay" ? `${card} in Google Pay` : card;
}

// heroSegments is the sentence under the check mark. Authorised means
// reserved, not charged: the box is captured when it ships (D-007). MB WAY
// captures at payment (S-07), so it says paid.
export function heroSegments(order, payment) {
  const amount = { text: formatAmount(payment.amount, order.currency), strong: true };
  const email = ` A confirmation is on its way to ${order.customer.email}.`;
  switch (payment.status) {
    case "Authorized":
      return [{ text: "We reserved " }, amount, { text: ` on ${cardPhrase(payment)}. You're charged when your box ships.${email}` }];
    case "Voided":
      return [{ text: "We released the " }, amount, { text: ` reserved on ${cardPhrase(payment)}. Nothing was charged.` }];
    case "Refunded":
    case "Partially Refunded":
      return [{ text: "We refunded " }, amount, { text: ` to ${cardPhrase(payment)}.` }];
    default: {
      const via = payment.method?.type === "card" ? cardPhrase(payment) : METHOD_NAMES[payment.method?.type] ?? "your payment method";
      return [{ text: "You paid " }, amount, { text: ` with ${via}.${email}` }];
    }
  }
}

// mainItem is the box itself, not an add-on: "Weekly box", "Christmas box".
function mainItem(order) {
  return order.items.find((it) => !it.add_on)?.name ?? "Your box";
}

// successFacts are the tiles under the hero. No delivery date (D-024).
export function successFacts(order, payment) {
  const recurring = isRecurring(order);
  return [
    { label: "Plan", value: recurring ? mainItem(order) : `${mainItem(order)}, one-off` },
    {
      label: "Next payment",
      value: recurring
        ? `${formatAmount(order.renewal_amount, order.currency)} a week, from your next box`
        : "None, this is a one-off order",
    },
    { label: "Paid with", value: methodLabel(payment) },
    { label: "Delivering to", value: `${order.customer.address.zip} ${order.customer.address.city}` },
  ];
}

export function planTag(order) {
  return isRecurring(order) ? "Weekly subscription" : "One-off order";
}

export function successTitle(order) {
  return `Thanks ${firstName(order.customer.name)}, your order ${order.id} is confirmed`;
}

// --- Failure banner -------------------------------------------------------

// failureBanner fills the checkout page's error banner. payment is the failed
// attempt when Checkout.com redirected to failure_url, or null after a Flow
// onError (no redirect, nothing to look up). The detail follows the response
// code: 20151 is "Cardholder failed 3D-Secure authentication".
export function failureBanner(payment, { order = null } = {}) {
  let detail = "Nothing was reserved on your card.";
  if (payment?.response_code === "20151") {
    detail = "Your bank couldn't confirm it was you, so nothing was reserved on your card.";
  } else if (payment?.response_code) {
    detail = "Your bank declined the payment, so nothing was reserved on your card.";
  }
  const help = "Try again below, or use another card or payment method.";
  const attempt = payment
    ? [order?.id, payment.payment_id, payment.status].filter(Boolean).join(" · ")
    : "";
  return {
    headline: "Your payment didn't go through.",
    detail,
    help,
    attempt: attempt ? `Previous attempt ${attempt}` : "",
  };
}

// --- Back office band (step 4.1a) -----------------------------------------

// formatDateTime shows Checkout.com's UTC times in Paris time, Panier Box's
// clock: "12 Nov 2026, 10:01".
export function formatDateTime(iso) {
  if (!iso) return "";
  return new Intl.DateTimeFormat(LOCALE, {
    timeZone: "Europe/Paris", day: "numeric", month: "short", year: "numeric",
    hour: "2-digit", minute: "2-digit",
  }).format(new Date(iso));
}

// renewalWeek reads the week from a renewal reference: ORD-1042-W3 gives 3.
// The CIT (ORD-1042) is week 1.
function renewalWeek(reference) {
  const m = /-W(\d+)$/.exec(reference ?? "");
  return m ? Number(m[1]) : 1;
}

// paidScheme is the network the order's paid first payment ran on. Only
// subscriptions record order.scheme, for renewals (D-029), so a one-off reads
// it from the payment itself. Failed attempts don't count.
function paidScheme(order) {
  if (order.scheme) return order.scheme;
  const cit = paymentsNewestFirst(order).find((p) => p.kind === "CIT" && pageState(p.status) === "paid");
  return cit?.method?.scheme ?? "";
}

// orderBar is the line above the actions: what renewals will reuse.
export function orderBar(order) {
  const recurring = isRecurring(order);
  let nextRenewal = "None, one-off order";
  if (recurring) nextRenewal = order.renewals_ready ? `Week ${order.next_renewal_week}` : "Off, no stored card";
  return [
    { label: "Order", value: order.id, mono: true },
    { label: "Basket", value: recurring ? "Subscription" : "One-off" },
    { label: "Stored card", value: order.source_id || "None", mono: Boolean(order.source_id) },
    { label: "Scheme", value: SCHEME_NAMES[paidScheme(order)] ?? "None" },
    { label: "Next renewal", value: nextRenewal },
  ];
}

// paymentsNewestFirst orders the status cards: latest renewal on top, the
// first box at the bottom. Retries of the same week (a decline, then a new
// try on the same order) keep the order they were stored in, newest first.
export function paymentsNewestFirst(order) {
  return (order.payments ?? [])
    .map((p, i) => ({ p, i }))
    .sort((a, b) => renewalWeek(b.p.reference) - renewalWeek(a.p.reference) || b.i - a.i)
    .map(({ p }) => p);
}

export function cardLabel(order, payment) {
  if (payment.kind === "MIT") return `Week ${renewalWeek(payment.reference)} renewal`;
  if (payment.kind === "CIT") return isRecurring(order) ? "First box" : "One-off box";
  return "Payment";
}

// statusClass maps a status to the mockup's badge colours.
const STATUS_CLASSES = {
  Authorized: "authorized", Pending: "pending", Captured: "captured", "Partially Captured": "captured",
  Voided: "voided", Refunded: "refunded", "Partially Refunded": "refunded",
  Declined: "declined", Canceled: "declined", Expired: "expired",
};

export function statusClass(status) {
  return STATUS_CLASSES[status] ?? "pending";
}

// Short scheme names for the badge, from processing.scheme first (D-027).
const SCHEME_BADGES = { visa: "VISA", mastercard: "MC", cartes_bancaires: "CB", amex: "AMEX" };

// methodLine is the card's method row: a scheme badge, "···· 4242 · Credit",
// and a G Pay badge when Google Pay was used. MB WAY has no card: text only.
export function methodLine(payment) {
  const m = payment.method ?? {};
  if (m.type !== "card") return { badge: "", text: METHOD_NAMES[m.type] ?? m.type ?? "", wallet: "" };
  const s = m.scheme || m.card_schemes?.[0] || "";
  const cardType = m.card_type ? m.card_type.charAt(0) + m.card_type.slice(1).toLowerCase() : "";
  return {
    badge: SCHEME_BADGES[s] ?? s.toUpperCase(),
    text: [m.last4 ? `···· ${m.last4}` : "", cardType].filter(Boolean).join(" · "),
    wallet: m.wallet === "googlepay" ? "G Pay" : "",
  };
}

// balanceCells: the four balances, greyed out when zero.
export function balanceCells(payment, currency) {
  const b = payment.balances ?? {};
  return [
    ["Authorised", b.authorized], ["Captured", b.captured], ["Voided", b.voided], ["Refunded", b.refunded],
  ].map(([label, v]) => ({ label, value: formatAmount(v ?? 0, currency), zero: !v }));
}

// 3DS authentication_response codes, in plain words. A renewal has no 3DS:
// no customer is there to authenticate.
const THREE_DS = { Y: "Y, authenticated", A: "A, attempted", N: "N, not authenticated", U: "U, unavailable", R: "R, rejected" };

export function paymentDetails(order, payment) {
  let threeDS = THREE_DS[payment.three_ds] ?? payment.three_ds;
  if (!payment.three_ds) threeDS = payment.kind === "MIT" ? "Not applicable (MIT)" : "Not applicable";
  const rows = [
    { term: "3DS", value: threeDS },
    { term: "Response", value: [payment.response_code, payment.response_summary].filter(Boolean).join(" ") || "None" },
  ];
  if (payment.expires_on) rows.push({ term: "Auth expires", value: formatDateTime(payment.expires_on) });
  // previous_payment_id on every renewal points at the first CIT (F6).
  if (payment.kind === "MIT" && order.cit_payment_id) {
    rows.push({ term: "Linked to", value: `${order.cit_payment_id} (first CIT)`, mono: true });
  }
  return rows;
}

// --- Ops actions (step 4.1b) ----------------------------------------------

function clock(date) {
  return new Intl.DateTimeFormat(LOCALE, { timeZone: "Europe/Paris", hour: "2-digit", minute: "2-digit" }).format(date);
}

// dispatchAction is the "Dispatch first box" panel. The server decides if
// there's something to capture (can_capture, capture_reason). sentAt is set
// after a successful click: the capture is asynchronous, so the payment can
// still say Authorized for a few seconds.
export function dispatchAction(order, { sentAt = null } = {}) {
  const recurring = isRecurring(order);
  const amount = formatAmount(order.amount, order.currency);
  const action = {
    title: recurring ? "Dispatch first box" : "Dispatch box",
    what: `Captures the ${amount} ${recurring ? "first " : ""}payment, like a warehouse scan would.`,
    enabled: Boolean(order.can_capture),
    reason: order.can_capture ? "" : order.capture_reason,
  };
  if (sentAt && order.can_capture) {
    action.enabled = false;
    action.reason = `Capture sent at ${clock(sentAt)}. Checkout.com captures in a few seconds.`;
  }
  return action;
}

// actionError is what an ops panel shows when an action fails: our message,
// then Checkout.com's HTTP status and error codes (F10). Never headers.
export function actionError(err) {
  const parts = [err?.message || "Something went wrong."];
  if (err?.ckoStatus) parts.push(`HTTP ${err.ckoStatus}`);
  if (err?.errorCodes?.length) parts.push(err.errorCodes.join(", "));
  return parts.join(" · ");
}

// --- Polling (step 4.2) ---------------------------------------------------

// pollUntil calls fetchOnce until isDone(result) is true, at most `tries`
// times, waiting intervalMs between tries (never after the last). Captures,
// voids and refunds are asynchronous at Checkout.com: the GET right after the
// action often still shows the old status (F5). A fetch error stops polling
// and is thrown: a 502 usually repeats, and every try costs two Checkout.com
// reads. sleep is passed in so tests run without real timers.
export async function pollUntil({ fetchOnce, isDone, tries = 10, intervalMs = 1000, sleep }) {
  let result;
  for (let i = 1; i <= tries; i++) {
    result = await fetchOnce();
    if (isDone(result)) return { result, done: true, tries: i };
    if (i < tries) await sleep(intervalMs);
  }
  return { result, done: false, tries };
}

// statusChangedFrom is the PRD's rule for when to stop: the payment's status
// is no longer the one it had before the action.
export function statusChangedFrom(before) {
  return (body) => body?.payment?.status !== before;
}

export const STILL_PROCESSING_MESSAGE = "Still processing at Checkout.com. Reload in a moment.";

// --- Renewal (step 4.3) ---------------------------------------------------

// renewAction is the "Run next week's renewal" panel. The server decides if
// the order can renew (can_renew, renew_reason). last is the renewal just
// run from this page: { week, payment }. An approved MIT moves the order to
// the next week. A declined one doesn't, and running it again sends the same
// key, so Checkout.com gives the same answer (D-034).
export function renewAction(order, last = null) {
  const action = {
    title: "Run next week's renewal",
    what: isRecurring(order)
      ? `${formatAmount(order.renewal_amount, order.currency)} MIT on the stored card. Reference ${order.id}-W${order.next_renewal_week}.`
      : "Renewals are for subscriptions only.",
    enabled: Boolean(order.can_renew),
    reason: order.can_renew ? "" : order.renew_reason,
  };
  if (last && order.can_renew) {
    const p = last.payment;
    const code = [p.response_code, p.response_summary].filter(Boolean).join(" ");
    action.reason = pageState(p.status) === "failed"
      ? `Week ${last.week} ${p.status.toLowerCase()}${code ? `: ${code}` : ""}. Running it again sends the same key, so the same answer.`
      : `Week ${last.week} ${p.status.toLowerCase()}. Next is week ${order.next_renewal_week}.`;
  }
  return action;
}

// --- Reverse on each card (step 4.4a) -------------------------------------

// reverseButton is a card's Reverse button, from the server's view of that
// payment: Reverse (void) before capture, Reverse (refund) after (F7).
// label "" means no button.
export function reverseButton(payment) {
  const labels = { void: "Reverse (void)", refund: "Reverse (refund)" };
  return { label: labels[payment.reverse_action] ?? "", reason: payment.reverse_reason ?? "" };
}

// --- Capture on each card (step 4.4b) -------------------------------------

// cardButtons lists a card's buttons, left to right: Capture when the server
// says the payment can be captured (no button otherwise: declined, voided,
// refunded, captured, MB WAY), then Reverse when there's something to reverse.
export function cardButtons(payment) {
  const buttons = [];
  if (payment.can_capture) buttons.push({ action: "capture", label: "Capture" });
  const rev = reverseButton(payment);
  if (rev.label) buttons.push({ action: "reverse", label: rev.label });
  return buttons;
}

// Live log (D-038, D-041). The server streams one entry per line: an action
// Panier Box sent, a webhook Checkout.com sent, or a read that moved a card.

export const LOG_LIMIT = 50; // same as the server's backlog

export function eventsUrl(orderId) {
  return `/api/events?order=${encodeURIComponent(orderId)}`;
}

// logTime is the entry's time in Paris, HH:MM:SS like the mockup.
export function logTime(iso) {
  if (!iso) return "";
  return new Intl.DateTimeFormat(LOCALE, {
    timeZone: "Europe/Paris", hour: "2-digit", minute: "2-digit", second: "2-digit",
  }).format(new Date(iso));
}

// httpPart colours Checkout.com's answer: green for 2xx, red otherwise.
// 0 means no answer came at all.
function httpPart(status) {
  if (!status) return null;
  return { text: String(status), cls: status < 400 ? "ok" : "bad" };
}

// logLine turns an entry into the mockup's line: time, arrow, then text
// parts, each with an optional colour class. The page sets them with
// textContent only. lineCls is "hook" for webhooks (orange) and "read" for
// reads (muted and indented: they're the GET behind an action or webhook).
export function logLine(entry) {
  const parts = [];
  const add = (text, cls = "") => { if (text) parts.push({ text, cls }); };
  let lineCls = "";

  if (entry.kind === "webhook") {
    lineCls = "hook";
    if (entry.http_status === 401) {
      // A forged webhook: we never read its body, so there's nothing else to show.
      add("bad signature", "bad");
      add("401, ignored", "id");
    } else {
      add(entry.text, "ev");
      add([entry.event_id, entry.payment_id].filter(Boolean).join(" "), "id");
    }
  } else {
    if (entry.kind === "read") lineCls = "read";
    add(entry.text);
    const http = httpPart(entry.http_status);
    if (http) parts.push(http);
    // ps_ IDs are shown muted like the mockup. "MIT ORD-1042-W2", "Void" and
    // "no response" are plain text, red for no response.
    if (entry.detail === "no response") add(entry.detail, "bad");
    else add(entry.detail, entry.detail?.startsWith("ps_") ? "id" : "");
    add(entry.status);
    for (const code of entry.error_codes ?? []) add(code, "bad");
  }
  return { time: logTime(entry.time), dir: entry.dir || "", lineCls, parts };
}

// liveBadge is the panel's connection badge. EventSource reconnects by
// itself, so "Reconnecting…" is normally brief.
export function liveBadge(connected) {
  return connected ? { text: "Live", cls: "live" } : { text: "Reconnecting…", cls: "live off" };
}

// refreshesOrder says whether a line means the cards changed: a read line is
// a stored status change, whether a webhook or this page's own poll caused
// it. The page then re-reads its order from our store (no Checkout.com call).
export function refreshesOrder(entry) {
  return entry.kind === "read";
}

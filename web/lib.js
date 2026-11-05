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

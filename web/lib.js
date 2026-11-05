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
// environment come from /api/config. Appearance is added in step 2.6b.
export function flowOptions(config, paymentSession, { onPaymentCompleted, onError }) {
  return {
    publicKey: config.publicKey,
    environment: config.environment,
    locale: LOCALE,
    paymentSession,
    onPaymentCompleted,
    onError,
  };
}

// A plain message for Flow's onError. Flow keeps the form, so Marie can try again.
export const PAYMENT_ERROR_MESSAGE =
  "Your payment didn't go through. Check your details or try another payment method.";

export const LOAD_ERROR_MESSAGE =
  "We couldn't load the payment form. Please refresh the page.";

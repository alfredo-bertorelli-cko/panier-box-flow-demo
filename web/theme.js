// Flow appearance object. It mirrors the :root tokens in web/styles.css, so
// Flow looks like part of the Panier Box page, not a foreign widget.
// Change a token in styles.css and here together: web/theme.test.js fails if
// the two disagree. Keys checked against Flow's Appearance docs (D-024).

// --font. A system stack on purpose: Flow can't load custom web fonts into the
// card number, expiry and CVV fields, so page and Flow match without one.
const font = '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif';
// No lineHeight: a unitless --lh (1.45) made Flow's card inputs about three
// times too tall (manual check, 2026-11-05). Flow's own line heights apply.

export const appearance = {
  colorBackground: "#FAF7F0", // --c-bg
  colorFormBackground: "#FFFFFF", // --c-surface
  colorPrimary: "#1E1E1C", // --c-text
  colorSecondary: "#5E5A53", // --c-muted
  colorBorder: "#CFC8BA", // --c-border
  colorFormBorder: "#8A857C", // --c-input-border
  colorAction: "#1E5B36", // --c-brand: pay button, selected method
  // Focus ring. Not a token: a softer green than --c-brand, so the ring reads
  // a little thinner (Flow has no focus width key). Agreed 2026-11-05. Must keep
  // 3:1 contrast on white for keyboard users (WCAG 1.4.11), see theme.test.js.
  colorOutline: "#4F8461",
  colorInverse: "#FFFFFF", // --c-on-brand: text on the pay button
  colorError: "#B3261E", // --c-error
  colorSuccess: "#1E5B36", // --c-success
  colorDisabled: "#B9B4AA", // --c-disabled

  // [--r-input, --r-button]. The docs don't say which index is which.
  // Confirmed by eye 2026-11-05: index 0 is the fields, index 1 the pay button.
  borderRadius: ["4px", "8px"],

  // Pay button: same as the page's .btn (700, 17px).
  button: { fontFamily: font, fontSize: "17px", fontWeight: 700, letterSpacing: 0 },
  label: { fontFamily: font, fontSize: "15px", fontWeight: 500, letterSpacing: 0 }, // --fs-label, --fw-label
  input: { fontFamily: font, fontSize: "16px", fontWeight: 400, letterSpacing: 0 }, // --fs-body
  // Payment method names ("Card", "Google Pay"), like the spec's method rows.
  subheading: { fontFamily: font, fontSize: "16px", fontWeight: 500, letterSpacing: 0 },
  footnote: { fontFamily: font, fontSize: "14px", fontWeight: 400, letterSpacing: 0 }, // --fs-small
};

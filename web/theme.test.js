// Proves web/theme.js mirrors the tokens in web/styles.css. Run: node --test web/
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { appearance } from "./theme.js";

// readTokens parses the :root block of styles.css into { "--c-bg": "#FAF7F0", ... }.
function readTokens() {
  const css = readFileSync(new URL("./styles.css", import.meta.url), "utf8");
  const root = css.match(/:root\s*\{([\s\S]*?)\n\}/);
  assert.ok(root, "no :root block in styles.css");
  const body = root[1].replace(/\/\*[\s\S]*?\*\//g, ""); // drop comments
  const tokens = {};
  for (const m of body.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) tokens[m[1]] = m[2].trim();
  return tokens;
}

// get("label.fontSize") or get("borderRadius.0") reads a nested appearance value.
const get = (path) => path.split(".").reduce((v, k) => v?.[k], appearance);

// The mirror itself: each appearance value and the token it must equal.
const MIRROR = [
  ["colorBackground", "--c-bg"],
  ["colorFormBackground", "--c-surface"],
  ["colorPrimary", "--c-text"],
  ["colorSecondary", "--c-muted"],
  ["colorBorder", "--c-border"],
  ["colorFormBorder", "--c-input-border"],
  ["colorAction", "--c-brand"],
  ["colorInverse", "--c-on-brand"],
  ["colorError", "--c-error"],
  ["colorSuccess", "--c-success"],
  ["colorDisabled", "--c-disabled"],
  ["borderRadius.0", "--r-input"],
  ["borderRadius.1", "--r-button"],
  ["button.fontWeight", "--fw-button"],
  ["label.fontSize", "--fs-label"],
  ["label.fontWeight", "--fw-label"],
  ["input.fontSize", "--fs-body"],
  ["subheading.fontSize", "--fs-body"],
  ["footnote.fontSize", "--fs-small"],
];
const FONTS = ["button", "label", "input", "subheading", "footnote"];
for (const f of FONTS) {
  MIRROR.push([`${f}.fontFamily`, "--font"]);
}

// Values with no :root token, and where they come from in the UI spec.
const NOT_TOKENS = {
  colorOutline: "#4F8461", // softer focus ring than --c-brand, agreed 2026-11-05
  "button.fontSize": "17px", // the .btn rule: font: 700 17px var(--font)
  "input.fontWeight": 400, // the body rule: font: 400 ...
  "footnote.fontWeight": 400, // the body rule
  "subheading.fontWeight": 500, // the spec's payment method rows: font-weight 500
};

test("every appearance value equals its styles.css token", () => {
  const tokens = readTokens();
  for (const [path, token] of MIRROR) {
    assert.ok(token in tokens, `${token} is not in styles.css :root`);
    // Compare as strings: CSS has "1.45" and "700", JS has the numbers.
    assert.equal(String(get(path)), tokens[token], `${path} should equal ${token} (${tokens[token]})`);
  }
});

test("values without a token match the spec rules they come from", () => {
  for (const [path, want] of Object.entries(NOT_TOKENS)) {
    assert.equal(get(path), want, path);
  }
  for (const f of FONTS) assert.equal(get(`${f}.letterSpacing`), 0, `${f}.letterSpacing`);
});

test("every appearance value is covered by the mirror or the exceptions", () => {
  // Walks every leaf, so a new key in theme.js can't skip the check above.
  const covered = new Set([...MIRROR.map(([p]) => p), ...Object.keys(NOT_TOKENS),
    ...FONTS.map((f) => `${f}.letterSpacing`)]);
  const leaves = [];
  const walk = (v, path) => {
    if (v !== null && typeof v === "object") for (const [k, x] of Object.entries(v)) walk(x, path ? `${path}.${k}` : k);
    else leaves.push(path);
  };
  walk(appearance, "");
  for (const p of leaves) assert.ok(covered.has(p), `${p} is not mirrored or explained`);
});

// WCAG 2.1 relative luminance and contrast ratio, for "#RRGGBB" colours.
function luminance(hex) {
  const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255)
    .map((c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}
const contrast = (a, b) => {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
};

test("focus ring keeps 3:1 contrast on the field background (WCAG 1.4.11)", () => {
  // Checks the formula on known pairs first, so a bug in it can't pass the real check.
  assert.equal(contrast("#000000", "#FFFFFF").toFixed(1), "21.0");
  assert.equal(contrast("#FFFFFF", "#FFFFFF").toFixed(1), "1.0");
  const ratio = contrast(appearance.colorOutline, appearance.colorFormBackground);
  assert.ok(ratio >= 3, `focus ring contrast ${ratio.toFixed(2)}:1 is below 3:1`);
});

test("no font block sets lineHeight", () => {
  // A unitless lineHeight (1.45, the --lh token) made Flow's card inputs about
  // three times too tall. Flow's default line heights are what the page needs.
  for (const f of FONTS) assert.ok(!("lineHeight" in appearance[f]), `${f}.lineHeight must not be set`);
});

test("appearance uses only documented Flow keys", () => {
  // Flow ignores unknown keys without an error, so a typo would go unnoticed.
  const documented = new Set([
    "colorAction", "colorBackground", "colorBorder", "colorDisabled", "colorError", "colorFormBackground",
    "colorFormBorder", "colorInverse", "colorOutline", "colorPrimary", "colorSecondary", "colorSuccess",
    "button", "footnote", "input", "label", "subheading", "borderRadius",
  ]);
  for (const k of Object.keys(appearance)) assert.ok(documented.has(k), `unknown Flow key ${k}`);
  const fontProps = new Set(["fontFamily", "fontSize", "fontWeight", "letterSpacing", "lineHeight"]);
  for (const f of FONTS) {
    for (const k of Object.keys(appearance[f])) assert.ok(fontProps.has(k), `unknown font property ${f}.${k}`);
  }
  assert.equal(appearance.borderRadius.length, 2);
});

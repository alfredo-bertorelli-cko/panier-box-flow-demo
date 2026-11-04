# CLAUDE.md

Panier Box Flow demo for a Checkout.com Implementation Engineer interview. Go back end with the Checkout.com Go SDK v3, plain JS front end, Checkout.com Flow in sandbox.

Read PRD.md and DECISIONS.md before any work. PRD.md says what to build and in what order. DECISIONS.md says why. Internal context is in `notes/internal-refs.md`.

## Hard rules: secrets and card data

* Never read, open, print, cat, grep or search `.env`. A deny rule in `.claude/settings.json` blocks reading it, but this rule covers every other route too, including scripts and `go run` output.
* Never print or log API keys, the webhook signature key, Authorization headers or full request headers. Not in code, logs, errors, tests, fixtures, docs, commit messages or chat. If a key is missing, name the variable only.
* Set the SDK logger so it can't print headers or keys. Check what it logs before the first sandbox call.
* Never put a real key or key-like value anywhere. Test keys are obviously fake (`sk_sbox_fake`). `.env.example` has names with empty values.
* Before saving any fixture to `testdata/`, check it for key-like strings (`sk_`, `pk_`, `Bearer`).
* I add the real keys to `.env` myself. If something needs a real key, write the test or command and I'll run it.
* The secret key is used only in Go. The browser only gets the public key, from `/api/config`.
* No raw card data anywhere. Card details go through Flow only. Panier Box stays on SAQ A.
* Load Flow only from `https://checkout-web-components.checkout.com/index.js`. Never download, bundle or self-host it.
* Log webhook events as event ID, type and payment ID only.
* Never commit `.env`, `data/`, `inspiration/` or `notes/`.

## How we work

* Follow the build plan in PRD.md section 12, one step at a time.
* Before each step: a short plan (files, functions, tests, API calls). Wait for my OK.
* During the step: write the tests with the code, not after. Run `go test ./...` (and `node --test web/` for JS) before saying it's done.
* After each step: in a few plain sentences, say what changed, what the tests prove and what they don't, the command to run them, and the manual check. Suggest a commit message. Then stop.
* If a step gets big, split it and tell me.
* I'm strong in Go but new to unit testing and front end. Teach as we go: explain why a test is shaped the way it is, and any Flow or browser behaviour that isn't obvious. Keep it short.
* If a test needs something from me (a test card, a payment ID, the tunnel URL, a sandbox response to use as a fixture), ask for it and say why.
* Small diffs. No refactors I didn't ask for.
* Go: standard library plus the Checkout.com SDK v3 and godotenv. Ask before adding anything else. Keep the SDK version pinned.
* JS: ES modules, no framework, no bundler, no npm. Pure helpers in `web/lib.js` so they can be tested with `node --test`.
* Clear names over clever code. Comment the payments "why" (for example why previous_payment_id points at the first CIT), not the obvious "what".
* Go errors: wrap with context. UI: a plain message.

## Checkout.com API and SDK

* Order of trust: API reference (and swagger.yaml), then docs, then SDK source. Links are in PRD.md section 14.
* Check every field against the API reference and the SDK struct before using it. Don't guess names.
* If the API reference, docs, SDK or PRD.md disagree, stop and tell me. Don't pick one quietly. Known example: SDK structs have lagged behind the API reference before.
* Handlers use the `PaymentsAPI` interface, never the SDK directly. That keeps them testable with a fake.
* Contract tests point the SDK at an `httptest` server with `configuration.NewEnvironment` and check the real JSON and headers sent. Use these for anything where a dropped field would be silent, like `capture: false`.
* Amounts in minor units (5000 = EUR 50.00), always from server-side basket data.
* Idempotency keys from stable IDs (`capture-{payment_id}`, `renewal-{order_id}-W{n}`, `reverse-{payment_id}`). Never random.
* Payment status comes from GET payment details. Webhooks trigger that GET. They don't set status directly.
* Sandbox only. Never point at production.

## Decision log

* When you make or find a decision (a field that works differently, a workaround, a scope change), add a D entry to DECISIONS.md: status and date, context, choice, why, trade-off. Keep it short.
* Sandbox findings go in the entry they affect (P-8, P-11, S-07 and so on).

## Writing style (README, docs, UI copy, comments)

* Plain English, short sentences, no buzzwords.
* Never use the em dash. Use a period, comma, or "and" or "but".
* UI copy in English with French details (EUR, Paris address).

## UI inspiration

* Screenshots of French meal-kit checkouts are in `inspiration/`. Look at them before UI work in steps 2.6 and 3.3. Use them for layout and tone only. No copying logos, images or text.

## Commands

* Run: `go run .`
* Unit, handler and contract tests: `go test ./...`
* Race check: `go test -race ./...`
* Sandbox tests (I run these): `go test -tags sandbox -run <TestName> ./...`
* JS helpers: `node --test web/`
* Vet and format: `go vet ./...`, `gofmt -w .`
* Git: don't commit or push. Suggest a commit message. I commit myself.

---
name: api-recon
description: Call this skill when collecting a website's API surface.
---

# API Recon (Frontend Interface Reconnaissance)

On an **authorized** target, discover as completely as possible: **backend APIs** (paths, methods, params, response bodies), **frontend routes**, and **UI feature triggers** (tabs, dialogs, table actions, etc.).

---

## Scope and Prohibitions (Agent must read · violating them is out of scope)

This skill covers **API / parameter-surface recon only** — not vulnerability hunting or exploitation.

### Task boundaries

| Scope | Allowed | Prohibited |
|---|---|---|
| **Target** | Enumerate paths, methods, params, routes, UI triggers | SQLi/XSS/broken-access/brute-force/fuzzing, request tampering, destructive actions |
| **Auth** | Hook + stub/mock past the **client-side** login gate | Asking the user for or guessing credentials; submitting real login forms |
| **Runtime** | Hook interfaces without credentials; use mock responses to get the SPA into its post-login shell | Flows that can only proceed with a real backend session |

### Credential-free dynamic analysis (Phase 3 default)

1. Use `preload.js` / `runtime_harvest.js` to **intercept and stub** login, permission, menu, and other bootstrap endpoints;
2. Return mock bodies with **correct shape, success business code, nullable data** for business query endpoints;
3. Let the frontend render post-login pages even with no backend or under 401, so more XHR/fetch/WebSocket fires;
4. **Empty data, blank tables, and placeholder UI are all expected** — never pivot to real login or vulnerability testing because of them.

**In one line**: use mocks to open up frontend routes and component mounts, **record outbound requests only**; what the backend returns does not matter — what matters is **which other endpoints the frontend calls**.

### Hard process prohibitions

| Prohibited | Do instead |
|---|---|
| Grep/curl/Read the main entry `index-*.js` for API paths before Phase 1 completes | Run `OUTDIR/harvest_static.py` |
| Hand-write `extract_apis.py`-style harvest replacements | Edit `OUTDIR/harvest_static.py` and re-run |
| Retry the same failing grep/command ≥2 times | Switch tactics: read tool_logs, edit harvest, check reference |
| Skip gates A/B and run `scripts/` originals directly | Copy to OUTDIR and adapt to the target |
| Real usernames/passwords, OTP, OAuth, or other auth | stub/mock (see above) |
| Skip stubs "to get real data" and run access/injection tests | Record outbound only — that is the recon boundary |
| Deletion, sensitive-data export, bulk writes, or other irreversible ops | Same for coverage clicks |
| Claim full page/API coverage without finishing runtime + dynamic enumeration | See "Definition of done" or note the limits |
| Claim full param knowledge without the trigger matrix + diff | Phase 3b matrix + Phase 5 diff |
| Infer required/optional from a single runtime sample | Multi-sample diff or validation-rule/error inference |

---

## Two layers + run modes

| Layer | Output | Limits |
|---|---|---|
| **Static** (JS bundle) | Full endpoint paths, route drafts, packet-building field candidates | No HTTP methods; params need Phase 1b; misses runtime-concatenated URLs |
| **Runtime** (live session) | Method + body + response + dynamic URLs + WS/SSE; multi-sample diff completes params | Pages only send requests once actually rendered; one sample cannot settle required/optional |

| Run mode | Engine | Fits |
|---|---|---|
| **depth** | `runtime_harvest.js` (Puppeteer) | API inventory, METHOD/params/bodies, WS/SSE, reproducible batch runs |
| **coverage** | browser + `preload.js` | Click tabs/dialogs/tables for deeper feature coverage |
| **both** | depth first, then coverage | Most complete, slowest |

**Parameter methodology** (no generic script): paths via harvest/regex; params via **anchor-expand + UI binding chain + multi-sample diff + error inference** (grep recipes in [reference.md](reference.md), section J).

---

## Definition of done

All boxes must hold before claiming recon is complete:

- [ ] **Static**: Phase 1 harvest produced `api_static.txt`, `routes.txt`, `js/`
- [ ] **Runtime**: at least depth or coverage; coverage/both requires **hooks active + dynamic enumeration loop**
- [ ] **Inside the shell**: visiting a business path does not land on `/login` (watch hash routes)
- [ ] **Params**: coverage/both finished the trigger matrix + `param_samples.json`; Phase 5 merged `params_merged.json`
- [ ] **Depth** (if module pages are blank): Phase 4 permission-tree restore and re-run until **module-level APIs** appear (not just locale/bootstrap)
- [ ] **Delivery**: Phase 5 outputs complete (see the Phase 5 output table); `insert_assets` wrote service and endpoint assets

---

## Scripts and gates

`scripts/` holds reference templates only — **never** run the originals directly as final results.

**Rule**: read first → adapt to the target → write into `OUTDIR` (e.g. `recon/`) → log in `CHANGES.md`; if nothing fits, rewrite following the methodology and borrow only structure.

| Gate | When | Reference script → OUTDIR copy | Common must-edits |
|---|---|---|---|
| **A (static)** | After Phase 0, before the **first** harvest/spider run | `harvest_static.py` / `spider_mpa.py` | **Default regex runs as-is on most sites**; only touch endpoint regex, webpack/Vite `publicPath`, MPA exclude/cookie when manifest/dialect mismatches |
| **B (runtime)** | After Phase 2, before depth/coverage | `runtime_harvest.js` / `preload.js` + `config.json` | Cookie/localStorage keys, neutralize success values, stubs, login regex, api prefixes, hash/history |

**SPA mandatory order** (not swappable; Phase numbers outrank "explore before scripting"):

| Step | Must | Must not |
|---|---|---|
| After Phase 0 | Next Bash = `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | curl/grep/Read the main entry `index-*.js` (usually >500KB) |
| Gate A | Copy script → small edits as needed → **run immediately** | Hand-extract APIs first, then decide whether to harvest |
| Before Phase 1 done | Verify output with `wc -l`; on 404s fix harvest and retry | Hand-write extract scripts; grep repeatedly over undownloaded URLs |
| From Phase 1b | Grep only `OUTDIR/js/*.js` | Substitute the main bundle for harvest |

- ✅ Copy `harvest_static.py` → (optionally) tweak regex → **run immediately**
- ❌ curl the main bundle → grep many times → write a throwaway extract → harvest last
- **MPA**: after Phase 0 the next Bash = `python3 OUTDIR/spider_mpa.py ...`

---

## Tooling and output limits

| Limit | Explanation |
|---|---|
| Big files | `index-*.js` over 100KB must **never** enter context via Read/grep; batch-process with OUTDIR scripts |
| grep output | Always `\| head -20` or `-m 5`; keep only path summaries in chat, never paste bundle fragments |
| Verification | Use `wc -l`, `ls \| wc -l`; never Read whole directories |
| Regex scouting | Optional, ≤1 shot, only on ≤50KB chunks or HTML; harvest is authoritative for statics |
| reference | Recipes/templates/troubleshooting live in [reference.md](reference.md) — do not re-inline them in full |

---

## Execution roadmap

```
Phase 0 triage + OUTDIR
  → Gate A → Phase 1 harvest (★ run immediately ★)
  → Phase 1b param reverse-engineering
  → Phase 2 three auth gates → config.json
  → Gate B → Phase 3 runtime + param matrix
  → Phase 4 permission tree (if needed) → re-run Phase 3
  → Phase 5 merge report + insert_assets: bulk-insert every discovered service/endpoint asset — never drop a discovered asset at insert time
```

Check off in order; **never enter the next Phase with the previous one unfinished**.

1. [ ] **Phase 0**: triage SPA/MPA; create `OUTDIR` → [Phase 0](#phase-0--classification)
2. [ ] **Gate A + Phase 1**: copy scripts → harvest **immediately** → `wc -l` verify → [Phase 1](#phase-1--static)
3. [ ] **Phase 1b**: anchor-expand + binding layer → `param_candidates.json` → [Phase 1b](#phase-1b--param-reverse-engineering)
4. [ ] **Phase 2**: three auth gates → `config.json` → [Phase 2](#phase-2--auth-gates)
5. [ ] **Gate B**: tune runtime scripts → [Phase 3](#phase-3--runtime)
6. [ ] **Phase 3**: depth / coverage / both; confirm inside-shell; param trigger matrix → `param_samples.json`
7. [ ] **Phase 4** (if needed): permission tree → patch stubs → re-run Phase 3 → [Phase 4](#phase-4--permission-tree-restore)
8. [ ] **Phase 5**: merged outputs + report + `insert_assets` → [Phase 5](#phase-5--merge-and-report)

---

## Phase 0 — Classification

Fetch the entry HTML and **create `OUTDIR`** (never modify `scripts/` inside the skill):

- **SPA**: empty shell + `<div id=app>` + chunks → Phases 1–5
- **MPA**: SSR + `<form>`, no endpoint bundle → after Gate A:

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

Produces `forms.txt`, `links.txt`, `api_inline.txt`. If an SPA has ≈0 forms → switch to Phase 1.

---

## Phase 1 — Static

Follow [Scripts and gates](#scripts-and-gates) · [Tooling and output limits](#tooling-and-output-limits).

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

harvest: parse HTML scripts → webpack/Vite manifest → download every lazy chunk → produce `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt`.

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- Chunk count vs manifest: 404s mean fix harvest and retry — never hand-curl chunks one by one
- Too few lines in `api_static.txt` → widen the OUTDIR endpoint regex and re-run (see reference)

### Phase 1b — Param reverse-engineering

Paths come from Phase 1; param fields need their own recon. Grep rules: see [Tooling and output limits](#tooling-and-output-limits).

**Done-standard**: key interfaces must answer — field names, transport location, inferred types, required or not, sample values, confidence.

#### 1b.0 — Transport shape

| Shape | Where params live | Static first look |
|---|---|---|
| REST JSON | body + query | `(params\|data\|body)\s*:\s*\{` next to the path anchor |
| GraphQL | `variables` | gql template, `$page: Int` |
| Classic form | urlencoded | `<form>`, `FormData` |
| File upload | multipart | `FormData.append` |
| Path params | `/user/:id` | Route table + `useParams` / `$route.params` |
| Encrypted/signed | packed into `sign`/`data` | Hook the crypto function's args (reference, section D) |

Output: label each endpoint `transport: query|json|form|graphql|encrypted`.

#### 1b.1 — Anchor-expand

Expand a window around a known path to find the packet-building object:

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| Wrapper layer | Param clues |
|---|---|
| axios instance | `data` / `params` |
| Unified request | Interceptor-injected global fields |
| OpenAPI client | Generated method signatures |
| React Query / SWR | Hook second argument |
| Vue composable | Composable args |

Type residue: `yup`/`zod`/rules, `Form.Item name=`, embedded Swagger.

→ `param_candidates.json`: `{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — Binding layer

```
Form field → onFinish/handleSubmit → transform → API payload
```

| Binding source | Technique |
|---|---|
| Form submit | Follow submit → transform → API |
| Table search | `getFieldsValue()` → `params` |
| Routes | `:id` / `?tab=` |
| Interceptors | Global `tenantId`, paging, sign |
| Enum selects | `options` → API enum values |

Trace the packet-building function upward from `fetch`/`XHR.send` in the DevTools call stack.

#### 1b.3 — Three packet questions (≠ the Phase 2 auth gates)

| Question | Must answer |
|---|---|
| **Assembly** | Where the payload is built, transform traces |
| **Validation** | required, pattern, enum |
| **Transport** | path / query / body / multipart / headers |

The interceptor gate (Phase 2) doubles as a read of globally injected fields (Authorization, `X-Tenant-Id`, sign).

#### 1b.4 — Handoff to Phase 3

Candidate fields come from the static/binding layers; **required/optional/conditional deps** need the Phase 3 param matrix + diff + Phase 5 error inference.

---

## Phase 2 — Auth gates

Grep inside `OUTDIR/js/` (with `head`), write `config.json` (recipes in reference):

| Gate | Question | Keywords |
|---|---|---|
| **Render gate** | How is logged-in state detected? | `isLogin`, `getToken`, Cookie/localStorage |
| **Interceptor gate** | What redirects to `/login`? | `response_code`, `errno`, axios interceptor |
| **Content gate** | Where do menu/permissions come from? | `menu`, `permission`, `role`, `acl`, `routes` |

Never treat a localStorage key name as a credential — confirm it from chunks/request chains.

**Exit = Gate B**: conclusions land in `config.json`, plus edits to `OUTDIR/runtime_harvest.js` / `preload.js`.

### Phase 2b — API observation (optional)

Use the OUTDIR `preload.js` to confirm session key names, Authorization, nested API URLs:

| Config | Output |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | headers observation |
| `extractUrlsFromResponse: true` | sub-APIs inside responses |
| `observe.storageReads/cookieReads: true` | backfill config |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

Each coverage round exports: `__API_RECON_LOG__`, `__API_RECON_DETAIL__`, `__API_RECON_ROUTES__`, `__API_RECON_OBSERVE__`.

---

## Phase 3 — Runtime

Gate B must be passed; follow [Scope and prohibitions](#scope-and-prohibitions) · the credential-free mock strategy.

Set `"runtimeMode": "depth" | "coverage" | "both"` in `config.json` (template in reference).

### Hooks and stubs (shared by depth + coverage)

| Layer | Scope | Purpose |
|---|---|---|
| L1 precise | auth/permission/bootstrap stubs | Past the first-screen auth |
| L2 negative-fix | all JSON responses | Not-logged-in codes → success |
| L3 fallback | `/api` etc. missed by L1 | Empty success bodies to open the UI |

- **depth**: fake auth + `forward` to rewrite business codes + `stubs`; walk `routes` (hash/history); produce `runtime_api.json`
- **coverage**: **document-start** inject `preload.js` (CDP `addScriptToEvaluateOnNewDocument` or userscript)

Verify: `window.__API_RECON_PRELOAD__` exists; business paths do not bounce to `/login`.

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### 3b — coverage dynamic enumeration (mandatory)

1. Main nav/sidebar — click each item, wait 1–3s for network
2. Tabs — `role=tab`, `.ant-tabs-tab`
3. Tables — first-row view/edit/details
4. Toolbar — export, filter, create (**avoid irreversible deletes**)
5. Each new module — merge APIs/routes
6. SPA — controlled `pushState` for `routes.txt` paths not yet covered (MPA forbidden)

**Param trigger matrix** (mandatory): record once per operation type per module, **diff multiple samples**:

| Operation | Usually added params |
|---|---|
| List first screen | paging + default filters |
| Search click | keyword, filter |
| Advanced filters | more optionals |
| Create/edit | full entity |
| Bulk/export/sort | `ids[]`, `exportType`, `sortField` |

**Outbound body/headers stay real under stubs** — trust the request. Record → `scan_raw.json`, `param_samples.json`, `api_detail.json`.

- **Vue**: `neutralizeVueRouter: true` + document-start preload
- **React**: `routes.txt` + sidebar clicks + `pushState`
- **both**: 3a depth first, then 3b coverage

---

## Phase 4 — Permission-tree restore

**Trigger**: blank module pages / only bootstrap per route (e.g. locale) → content gate not passed.

| Symptom | Meaning |
|---|---|
| Inside shell OK | Render gate + interceptor gate passed |
| Sidebar missing items / blank clicks | stub shape or permission codes incomplete |
| Same tiny API set on every route | `v-if permission` not passing |
| `routes.txt` far shorter than bundle | Must complete from the auth module |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

Typical chain: `role_permissions` (flat codes) + `permissions/all` (tree) → `getResultTree` → `userRouteAuth[CODE].url`.

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Intermediate outputs: `route_map.json`, `userRouteAuth.json`, `permissions_tree.json`, `*_stub.json`, `perm_codes_all.txt`.

Stub check: outer `response_code` matches the interceptor gate; flat codes align with the tree; `routes` covers every link in `route_map`.

Update `config.json`, then **re-run Phase 3**. Large SPAs may tune `waitUntil`, `routeTimeout`, `perRouteMs` (see reference, sections A3/I).

---

## Phase 5 — Merge and report

### Output table

| File | Phase | Contents |
|---|---|---|
| `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` | 1 | Static bundle and paths |
| `param_candidates.json` | 1b | Static param field candidates |
| `config.json` | 2 | Three gates + runtime config |
| `runtime_api.json` | 3a | depth detail recording (incl. WS/SSE) |
| `param_samples.json`, `scan_raw.json`, `api_detail.json` | 3b | Multi-samples, click logs, detail |
| `route_map.json` etc. | 4 | Permission-tree intermediates (if run) |
| `params_merged.json` | 5 | Merged param fields + confidence |
| `api_merged.txt` | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| `site_map.json` | 5 | Routes, APIs, params, feature points, limits |
| **insert_assets** | 5 | Write all service + endpoint assets into the asset inventory |

### 5b — Param merging

Diff from `param_samples.json`; **no generic merge script**. Confidence rules in reference J7 (high/medium/low/pending-trigger).

### 5c — Error inference

Within scope, incomplete requests may read 400s (**param recon, not vulnerability testing**): `field 'x' is required`, enum errors, etc. Watch the `data` wrapper, `variables`, pre-encryption `bizData`.

Reports must note: runtimeMode, static/runtime API counts, param confidence, uncovered modules, `CHANGES.md` summary relative to the reference scripts.

Suggested `site_map.json` shape:

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

More fields and grep recipes: see [reference.md](reference.md).

---

## General notes

- **Framework-agnostic**: same approach for webpack/Vite/Angular lazy loading
- **Transports**: REST/JSON, GraphQL, WebSocket, SSE; gRPC-web out of scope
- **SSR**: client-side fetch is recordable; RSC/Server Actions not fully enumerable
- **Blind spots**: JSVMP, WASM, HMAC/mTLS hard checks → static + note the limits
- **Param blind spots**: conditional linkage, hidden params, WASM packet-building → "pending-trigger" / "unreachable"
- **Static is the safety net**: statics still enumerate endpoints when runtime is blocked

---

## Additional resources

- Grep recipes, `config.json` template, troubleshooting, hooks, param reverse-engineering (section J), site_map template: **[reference.md](reference.md)**
- Reference script paths: see the [Scripts and gates](#scripts-and-gates) table

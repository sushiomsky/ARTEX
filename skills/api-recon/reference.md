# api-recon — Reference Manual

Grep recipes, `config.json` templates, and troubleshooting. Run all greps against the `js/` directory. For single-line bundles, `js-beautify` first or `sed 's/}/}\n/g'`; usually a raw grep with a context window suffices.

## Script Notes

Every file under `scripts/` is a **reference template** that must be adapted to the target site before running. Typical change points:

| Script | Common edits |
|---|---|
| `harvest_static.py` | endpoint regex, webpack/Vite manifest parsing, micro-frontend publicPath, retry/concurrency |
| `runtime_harvest.js` | neutralize field names + success values, stub match rules + body shapes, routes source, WS recording, `waitUntil`/`routeTimeout`/`proxy` |
| `preload.js` | `loginPathRe`, L1 stubs, `neutralize.fields`, `apiPattern`, whether L3 is on, `recordDetail`, `observe.*`, `neutralizeVueRouter` |
| `spider_mpa.py` | `--exclude` destructive links, cookie, depth/max, same-origin filtering |
| `extract_route_map.py` | `routeMap` / `routeLink` regex, KEY naming patterns |
| `build_perm_tree.py` | `userRouteAuth` parsing, `ROOTS`/`PREFIX_PARENT` hierarchy heuristics, stub outer field names |
| `config.json` | Single entry point for all of the above site-specific params |

Keep adapted files in the task workdir (e.g. `recon/`) and note the exact diffs vs the reference scripts in the report.

---

## A. Reverse-Engineering the Three Gates

### A1. Render gate — "How is logged-in state detected?"

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

Trace the chain `isLogin = f(getUser())` → `getUser = decode(storage.read(KEY))` to pin down the **storage key**, the **container** (Cookie vs localStorage), and the **encoding**:

| Encoding | config forge approach |
|---|---|
| Plaintext string / `"1"` / token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | Unsigned / `alg:none` JWT, or sign with the in-bundle key |
| Encrypted (SM2/AES/RSA) | Find the hardcoded key; the render gate can forge when only a decodable blob is needed; otherwise fall back to statics |

→ Write into `cookies` / `localStorage`.

### A2. Interceptor gate — "What redirects to /login?"

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(未登录|请重新登录|登录已过期|unauthorized|登录失效|授权|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

Pin down: the **field name**, the **success value** (usually `0` or `200`), and the **failure values that trigger redirect**. Verify with a junk session:

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

→ Write into `neutralize.fields` + `neutralize.success`.

### A3. Content gate — "Where do menu/permissions come from?"

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

**Two data layers** (typical enterprise admin):

| API | Typical payload | Consumer |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | Route guards, button-level ACL |
| `.../permissions/all` | `tree[{ code, position, children }]` | Sidebar menu rendering |
| In-bundle `userRouteAuth` | `{ CODE: { url, name? } }` | code → frontend path |
| In-bundle `routeMap` | `{ KEY: { name, link } }` | Alias resolution (webpack `o.DASHBOARD`) |

Read the consumer code to confirm: how `getResultTree(tree, permissions)` filters, which field `v-if` / `hasAuth(code)` checks.

**Manual forge** (small sites): build a permissive payload → `stubs`.

**Full permission-tree restore** (large sites with still-blank sidebar/submodules): see **section I**.

---

## B. config.json Template

```json
{
  "baseUrl": "https://target/",
  "runtimeMode": "both",
  "chromium": "/usr/bin/chromium",

  "cookies": [
    { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
  ],
  "localStorage": { "token": "faketoken", "isLogin": "1" },

  "neutralize": {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": { "success": true, "message": "ok" }
  },
  "forward": true,
  "loginUrlPattern": "/login",
  "apiPattern": "/api/|/rest/|/graphql",

  "mockTier": "L1+L2",
  "recordDetail": true,
  "observe": {
    "storageReads": false,
    "cookieReads": false,
    "xhrHeaders": true
  },
  "neutralizeVueRouter": true,
  "stubs": [
    {
      "match": "permissions/all|/menu|role_permissions",
      "body": {
        "response_code": 0, "code": 0,
        "data": {
          "permissions": ["*"],
          "menus": [
            { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
            { "name": "alert", "path": "/alert", "show": true, "children": [] }
          ]
        }
      }
    }
  ],

  "explore": {
    "clickTabs": true,
    "clickTables": true,
    "pushStateFallback": true,
    "maxMenuItems": 50
  },

  "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
  "waitMs": 1500, "perRouteMs": 900, "headless": true,
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "proxy": "",

  "captureResponses": true, "recordWs": true, "respMax": 600
}
```

Field notes:
- `runtimeMode`: `depth` (Puppeteer), `coverage` (browser MCP), `both`
- `cookies[].value` prefixes: `b64json:` → base64(JSON); `json:` → raw JSON; no prefix → literal
- `forward: true` forwards real requests and rewrites code fields; `false` fully offline stub
- `mockTier`: coverage-mode preload enablement level, e.g. `L1+L2`, `L1+L2+L3`
- `routes` comes from `routes.txt`; the harness auto-appends `<a href>` after forging menus
- `captureResponses` / `recordWs` only take effect in depth mode
- `waitUntil`: large SPAs use `domcontentloaded`; `networkidle2` tends to hang
- `routeTimeout`: per-route `page.goto` timeout (ms)
- `proxy`: Puppeteer `--proxy-server`; `HTTP_PROXY` / `HTTPS_PROXY` also work

### B1. Dual-stub template (role_permissions + permissions/all)

```json
"stubs": [
  {
    "match": "role_permissions",
    "body": {
      "response_code": 0,
      "data": {
        "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
        "role_type": "SUPER_ADMIN"
      }
    }
  },
  {
    "match": "permissions/all",
    "body": {
      "response_code": 0,
      "data": [
        {
          "code": "MONITOR",
          "position": 1,
          "children": [
            { "code": "MONITOR_ALERT", "position": 1, "children": [] }
          ]
        }
      ]
    }
  }
]
```

Outer field names (`response_code` / `code` / `data`) must match the A2 interceptor gate; `permissions` must cover every leaf code in the tree.

---

## C. coverage Mode: preload Config

Edit the top `CONFIG` object in `scripts/preload.js`, or substitute before CDP injection:

```javascript
const CONFIG = {
  loginPathRe: /\/(login|signin)(\/|$|\?)/i,
  mockTier: 'L1+L2',
  forward: true,
  recordDetail: true,
  extractUrlsFromResponse: true,
  neutralizeVueRouter: true,
  observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
  neutralize: { fields: ['response_code', 'code'], success: 0 },
  stubs: [ /* same as config.json stubs */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

Verify: `window.__API_RECON_PRELOAD__ === true` and pathname stays put.

Export the recordings:

```javascript
JSON.stringify({
  apis: [...window.__API_RECON_LOG__],
  detail: window.__API_RECON_DETAIL__,
  routes: [...(window.__API_RECON_ROUTES__ || [])],
  observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

---

## D. preload / runtime Hook Capabilities

Browser hook capabilities built into preload (coverage) and runtime_harvest (depth), and their reach:

| Hook capability | Value for API discovery | Coverage |
|---|---|---|
| Hook fetch / XHR.open | Record request URL/method | ✅ `recordDetail` + `__API_RECON_LOG__` |
| Hook XHR.setRequestHeader | Discover Authorization and other headers | ✅ `observe.xhrHeaders` |
| Hook localStorage/cookie reads | Confirm session key names | ⚠️ Optional `observe.storageReads/cookieReads` |
| Vue route harvesting | Complete frontendRoutes | ✅ `__API_RECON_ROUTES__` (loaded routes) |
| Vue guard neutralize / login-redirect block | Open modules to trigger APIs | ✅ `neutralizeVueRouter` + native redirect neutralize |
| React route harvesting | Complete routes | ⚠️ Static + clicks; no dedicated hook |
| Page-redirect block (login paths) | Stay on page for analysis | ⚠️ Blocks login paths only — never business navigation |
| Hook crypto libs (CryptoJS/SM etc.) | Encrypted params → plaintext API body | ❌ Hook crypto function args manually; record conclusions in config |
| Anti-debug bypass | Otherwise runtime records no APIs | ❌ Handle manually; statics still work |

---

## E. Endpoint Extraction Regex (when statics are thin)

Widen `extract_endpoints` in `harvest_static.py`, or by hand:

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---

## F. Troubleshooting

| Symptom | Cause → fix |
|---|---|
| Very few static APIs | Endpoint dialect mismatch → widen regex (section D) |
| Chunk count ≪ manifest | CSS-only or undeployed chunks; 404s already retried |
| Runtime still shows the login page | Wrong render gate → recheck A1: key names, container, encoding, domain |
| Inside shell but modules blank | Content gate → forge menus (A3); `routes` paths may be wrong |
| Only bootstrap/locale per route | Incomplete permission codes → section I permission-tree restore; check the `role_permissions` + `permissions/all` dual stub |
| Sidebar has items but subpages blank | Tree missing intermediate nodes, or codes disagree with `userRouteAuth` |
| Every API bounces to login | Interceptor gate → confirm `neutralize`; nested fields need a wider walk |
| Zero WS frames | Subscribe only happens after user interaction; lengthen `perRouteMs` |
| Empty response bodies | Real responses exist only with `forward: true` |
| Chromium missing | Install chromium or set `config.chromium` / `CHROMIUM` |
| Many mocks yet back to login | Hook fired too late or missing the `location.href` setter → document-start + preload |
| All lists empty | L3 empty arrays are normal; keep clicking tabs/settings/details |
| Mistook a Redux action for a route | Filter internal paths containing get/set/change/clear/toggle/upload |
| Vue still bounces to login | preload not at document-start → change injection timing; or with `neutralizeVueRouter: false` clear guards manually |
| URLs in responses never enter the log | Turn on `extractUrlsFromResponse`; or hand-extract from `__API_RECON_DETAIL__` |
| Unknown Authorization header name | Turn on `observe.xhrHeaders` or inspect request headers in DevTools |
| Runtime very slow / timing out | Set `waitUntil: domcontentloaded`; lower `routeTimeout`; never `networkidle2` |
| Proxy connection fails | Check `proxy` / env vars; keep Puppeteer and curl proxy ports consistent |

---

## G. Hardened Targets

When the server progressively validates sessions (unsignable signature cookies, server-rendered unstubbable menus), runtime stalls at the shell. Expected behavior:

- **Statics still suffice for endpoint enumeration** — module paths are in the code
- With authorization, run the same harness on a **real session**: `forward: true`, no neutralize needed, capturing real methods/params/responses

---

## H. Single-Task Checklist

1. Confirm the authorization scope
2. **Read** `scripts/harvest_static.py` → adapt to the target → run → review `api_static.txt`, `routes.txt`
3. **Phase 1b**: path anchor-expand + binding layer → `param_candidates.json` (section J)
4. Reverse A1/A2/A3 → write the site-specific `config.json`
5. **Read and adapt** `runtime_harvest.js` / `preload.js` before executing
6. `runtimeMode=depth`: `npm install` → run the adapted harvest script
7. `runtimeMode=coverage/both`: document-start inject the adapted preload → browser MCP dynamic enumeration + **param trigger matrix**
8. Modules don't render → **section I permission-tree restore** → patch stubs → re-run
9. Multi-sample param diff + error inference → `params_merged.json`
10. Merge → `site_map.json` + `api_merged.txt`, honestly labeling coverage, gaps, and script change points

---

## I. Permission-Tree Restore (Phase 4 deep dive)

Use when forging simple `menus: [{ path, show: true }]` fails and submodules still don't mount.

### I1. Locate the auth module

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

Record: the **permission API path**, **response field names**, **consumer chunk filename**.

### I2. Extract routeMap

```bash
python3 scripts/extract_route_map.py recon/js recon/
# produces recon/route_map.json
```

If `[!] no routeMap pattern found`: widen the regex in `extract_route_map.py`, or grep by hand:

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. Build the permission tree + stub

```bash
python3 scripts/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Script logic:
1. Parse `userRouteAuth={MONITOR:{url:...},...}` (including webpack aliases like `He=o.DASHBOARD`)
2. Resolve alias → real path with `route_map.json`
3. Infer parent by code prefix (`MONITOR_ALERT` → `MONITOR`)
4. Emit `permissions_tree.json`, `permissions_all_stub.json`, `role_permissions_stub.json`
5. With `--config`, auto-write `config.json`'s `stubs` and extend `routes`

**Adapt to the target** (top of script):
- `DEFAULT_ROOTS`: top-level module code list
- `DEFAULT_PREFIX_PARENT`: `PREFIX_` → parent mapping
- `DEFAULT_EXTRA_PARENT`: orphan nodes without a prefix relation

### I4. Verify stub consistency

```bash
# permission count should ≈ userRouteAuth entry count
wc -l recon/perm_codes_all.txt
# routes should cover every route_map link
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. Re-run runtime and compare

```bash
node recon/runtime_harvest.js recon/config.json
# compare runtime_api.json counts before/after forging; check module APIs like /attack, /asset appear
```

| Before forge | After forge (success) |
|---|---|
| Same 3–5 bootstrap entries per route | Different routes trigger different module APIs |
| Only `/api/locale/language` | `/api/web/...` module endpoints appear |
| Single-digit routes in `routes.txt` | 80–110+ routes from route_map |

### I6. If it still fails

- **coverage mode**: click sidebar + tabs — permission gating may only request after interaction
- **stub fields**: compare the real API (curl + real session) vs stub nesting
- **Extra guards**: grep button-level checks like `hasPermission|checkRole|func.`, extend `role_permissions.permissions`
- **Static fallback**: module API paths are still in `api_static.txt`; runtime only adds METHOD/body; keep `param_candidates.json` + recorded samples for params

---

## J. Param Reverse-Engineering (Phases 1b / 5b / 5c)

**Methodology, not a generic script.** Find paths with regex; find params with anchor-expand + UI binding chain + multi-sample diff + error inference.

### J1. Anchor-expand — from path to packet builder

```bash
# anchor on Phase-1-known paths
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. Wrapper layers and transport shapes

```bash
# axios / unified request
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart
grep -rhoaE 'FormData|\.append\(' js | head

# path params
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. Validation gates — required / format / enum

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. Binding layer — form → API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

Runtime assist: DevTools → Network → request → **Initiator** (call stack) — trace the packet builder up from `fetch`/`send`.

### J5. Encrypted params

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

**Never guess fields on ciphertext** — hook the crypto function's **args** and record the plaintext payload pre-encryption; conclusions go in `config.json` / `param_candidates.json`.

### J6. Param trigger matrix (mandatory for Phase 3)

Record once per operation per module, diffing request body/query:

| Operation | Focus |
|---|---|
| List first screen | Paging defaults |
| Search | keyword, filters |
| Advanced filters | optional fields |
| Create/edit | Full entity |
| Bulk/export | `ids[]`, `exportType` |
| Sort/page | `sortField`, `order` |

Produces `param_samples.json`: `[{ "path", "method", "action": "search", "body", "query", "headers" }]`

### J7. Confidence rules

| Confidence | Condition |
|---|---|
| **High** | Static callsite + ≥2 consistent runtime samples |
| **Medium** | Static only, or a single runtime sample |
| **Low** | Response/error inference, not re-verified |
| **Pending-trigger** | Statically known fields the UI/permissions never reached |

### J8. Scenario quick-match

| Scenario | Order |
|---|---|
| REST list page | J1 packet object → J6 four-way diff → J3 rules |
| Create/edit form | J3 Form name → J4 submit chain → runtime submit + deliberately blank 400 |
| GraphQL | J2 variables declaration → record variables per operation at runtime |
| Encrypted body | J5 hook args → pre-encryption fields are the real params |

### J9. Mapping to api-recon phases

| api-recon | Param recon |
|---|---|
| Phase 1 static | J1 anchor-expand |
| Phase 2 A2 interceptor | Globally injected fields (tenantId, sign) |
| Phase 3 runtime | J6 trigger matrix + `param_samples.json` |
| Phase 4 permission tree | Different module forms differ → full fields only trigger with enough permission |
| Phase 5 merge | `params_merged.json` + confidence; never settle required on one sample |

### J10. Troubleshooting

| Symptom | Fix |
|---|---|
| Statics name fields runtime never shows | Label "pending-trigger"; grow the permission tree / click advanced filters / linked-select options |
| Same path, different body shapes | Normal — record per `action`, never force-merge schemas |
| Stub responses are fake but you want params | Read the **outbound request** body/headers — never infer backwards from stub responses |
| 400 reports a nested field | Watch the outer wrappers `data`/`bizData`/`variables` |
| GraphQL shows only the operation name | Expand the `variables` JSON; find `$var: Type` statically |

---

# FileBrowser Quantum — SGC Deployment Architecture

How the P3X-118 FileBrowser Quantum fork is built, packaged, deployed, and
secured across the SGC estate, and how **Authentik holds complete authority**
over who reaches a file share and what they can do there.

This is the design reference (the "why" and how-it-fits-together). For
day-to-day commands and code-navigation, see [`../CLAUDE.md`](../CLAUDE.md). For
enabling the service on a host, see the role's
`docs/configuring-filebrowser-quantum.md`.

---

## 1. Three layers, one lifecycle

The system is three repositories in a strict pipeline. Each owns one concern and
hands a versioned artifact to the next.

```
  ┌─────────────────────────────────────────────────────────────────────┐
  │ APP    github.com/P3X-118/filebrowser  (this repo)                   │
  │        Go backend + embedded Vue frontend → single binary → image    │
  │        Carries our fork features; main stays an upstream mirror.      │
  │        Artifact: p3x-118/filebrowser:<X.Y.Z-N>  (built on controller) │
  └───────────────────────────────┬─────────────────────────────────────┘
                                   │ image (docker save | ssh | docker load)
                                   │ version string  X.Y.Z-N
  ┌────────────────────────────────▼────────────────────────────────────┐
  │ ROLE   github.com/P3X-118/filebrowser-quantum-ar                     │
  │        MASH-pattern Ansible role: renders config.yaml/env/labels,     │
  │        runs the image docker-in-systemd (read-only, cap-drop-ALL).    │
  │        Artifact: role release  v<X.Y.Z-N>  (pinned in requirements)   │
  └───────────────────────────────┬─────────────────────────────────────┘
                                   │ galaxy fetch (just roles)
  ┌────────────────────────────────▼────────────────────────────────────┐
  │ PLAYBOOK   ~/sgc/SGC   (the MASH master playbook)                    │
  │        Per-host vars decide hostname, image, OIDC realm, gates.       │
  │        `just install-service filebrowser-quantum --limit <host>`      │
  └───────────────────────────────┬─────────────────────────────────────┘
                                   │ deploys to
  ┌────────────────────────────────▼────────────────────────────────────┐
  │ HOSTS   files.primebaseball.pro  ·  files.sgc.ai   (§7)               │
  └──────────────────────────────────────────────────────────────────────┘
```

**Why three layers.** The app repo must stay cleanly mergeable with the upstream
`gtsteffaniak/filebrowser` (we never rename the Go module path or import paths —
P3X-118 alignment lives in git remotes, image names, and the role layer, not in
source). The role encapsulates the runtime shape once so every host inherits it.
The playbook holds only what differs per host. A change flows strictly
downstream: app tag → role bump → host var.

---

## 2. The application (our fork)

FileBrowser Quantum compiles the Vue frontend into the Go binary via
`//go:embed`, so the deployed artifact is one self-contained executable.

```
  main.go → cmd.StartFilebrowser (cmd/root.go)
     │  config: -c flag → FILEBROWSER_CONFIG → ./config.yaml
     │  settings.Initialize → validate → global settings.Config
     ▼
  http/  (stdlib ServeMux, method patterns, middleware combinators)
     ├─ http/oidc.go ........ OIDC login: verify ID token (UserInfo fallback,
     │                        Authentik-friendly), map claims → user + groups
     ├─ embedded Vue SPA (backend/http/embed, built at image time)
     └─ /swagger ............ generated API docs
  storage:
     ├─ BoltDB .............. users, shares, settings, ACCESS RULES, API tokens
     └─ SQLite index ........ per-source file index (<cacheDir>/sql/index_all.db)
```

### What our fork adds (not upstream)

These three changes are what make Authentik the single source of authority.
All are additive and behavior-preserving when unconfigured, so `main` stays
upstream-mergeable.

| Tag | Feature | Effect |
|-----|---------|--------|
| `1.5.0-1` | `auth.methods.oidc.groupPermissions` | Map of IdP group → permission grants. Every login recomputes permissions = `userDefaults` floor + union of the user's groups' grants (grants **and** revocations). Permission management leaves the app entirely. Impl: `settings.PermissionsFromGroups` + `http/oidc.go`. |
| `1.5.0-2` | top-level `access.rules` | Declarative path ACLs in config, applied at startup by `cmd/access.go:applyConfigAccessRules`. Config owns the paths it declares (re-declare = replace, empty = clear); UI-made rules elsewhere untouched; referenced groups auto-created before their first member logs in; a bad declaration **fails startup** (fail-closed). |
| `1.5.0-2` | (config) `logoutRedirectUrl` | Wired to the IdP end-session URL for RP-initiated logout — one logout ends the Authentik session too. |

Upstream already provided `adminGroup` (admin from group membership, re-synced
each login), `userGroups` (login allow-list), `groupsClaim`, and
`SyncUserGroups` (reconciles IdP group names into the internal ACL group system
each login). Our additions complete the picture so **nothing access-related is
managed in the filebrowser UI**.

---

## 3. The Ansible role

`filebrowser-quantum-ar` is a MASH/MDAD-lineage role (var prefix
`filebrowser_quantum_`). It renders three files and a systemd unit, then runs the
image as docker-in-systemd.

- **Container shape:** read-only rootfs, `--cap-drop=ALL`, non-root
  `--user uid:gid`, no published host ports (reachable only on the Traefik
  docker network). Bind mounts: `cache → /home/filebrowser/cache`,
  `data → /home/filebrowser/data`, `files → /folder` (the single source).
- **The unit recreates the container on every restart** — so all state must live
  under the `data`/`cache` mounts. Role `v1.0.3-3` pins `server.database`,
  `cacheDir`, and the `FILEBROWSER_CONFIG`/`FILEBROWSER_DATABASE` env to absolute
  paths inside `data`/`cache`, so BoltDB persistence can't drift off the mount if
  the image's WORKDIR or baked-in defaults ever change.
- **Secrets** (OIDC client secret, any admin password) are injected via the `env`
  file only, never the rendered `config.yaml`.
- **Config escape hatch:** keys without a dedicated role var are set through
  `filebrowser_quantum_configuration_extension_yaml` (deep-merged into the
  rendered config) — this is where `groupPermissions`, `userGroups`,
  `logoutRedirectUrl`, and `access.rules` land.
- **Self-build path** (`..._self_build: true`) clones `P3X-118/filebrowser` and
  builds `_docker/Dockerfile` on the host — supported but unused; SGC ships
  controller-built images instead (see §8).

---

## 4. SGC playbook integration

Standard MASH 4-step wiring (`requirements.yml` → `setup.yml` →
`group_vars/mash_servers` systemd list → service vars block). The role is fetched
at `v1.0.3-3`. `group_vars` auto-wires Traefik labels and ONLYOFFICE (when both
services are enabled); each host's `vars.yml` provides hostname, image, OIDC
realm, and security gates.

---

## 5. The Authentik authority model

The design goal: **the identity provider + config-as-code are the complete
authority; the filebrowser UI manages nothing.** Every decision is recomputed
from the IdP on each login.

```
  Authentik (the source of truth)
     │  provider `<realm>-files`, `groups` scope → group-name list on every token
     ▼
  ┌──────────────┬───────────────────────────────────────────────────────────┐
  │ WHO may LOGIN │ userGroups: [..]   → 403 pre-user-creation if in none      │
  │              │   (shared IdP); dedicated-IdP realms scope by app binding   │
  ├──────────────┼───────────────────────────────────────────────────────────┤
  │ WHAT they can │ groupPermissions: {group → grants}                         │
  │   DO (caps)  │   perms = userDefaults floor ∪ groups' grants, every login  │
  ├──────────────┼───────────────────────────────────────────────────────────┤
  │ ADMIN        │ adminGroup → in-app admin, re-synced every login            │
  ├──────────────┼───────────────────────────────────────────────────────────┤
  │ WHERE (dirs) │ access.rules (config, boot) + SyncUserGroups reconciles     │
  │              │   IdP groups → internal ACL groups every login              │
  ├──────────────┼───────────────────────────────────────────────────────────┤
  │ LOGOUT       │ logoutRedirectUrl → Authentik end-session (RP-initiated)    │
  └──────────────┴───────────────────────────────────────────────────────────┘
```

Because every axis re-syncs on login, moving a user between Authentik groups
changes their filebrowser reality at their next sign-in — including revocation.
The only residual is an already-issued session JWT (≤ 2 h) that survives until it
expires; the recompute happens on next login.

**Provisioning** is scripted and idempotent:
`~/sgc/apps/authentik/scripts/sgc/provision-filebrowser.py` (run via
`run-provision.sh`). It creates the OAuth2 provider + application + groups. Env
overrides (`FILEBROWSER_PROVIDER_NAME` / `APP_SLUG` / `EDITORS_GROUP` /
`OIDC_ADMIN_GROUP` / `ISSUER_HOST` / `REDIRECTS`) let one script serve multiple
realms on the shared IdP. The client secret is derived from the host's
`sgc_pgsk` (salt `filebrowser.oidc`) on **both** sides — the role var and the
provisioner's `FILEBROWSER_OIDC_SECRET` — so they match with no manual copying.

---

## 6. Request path and the security gates

A request crosses independent gates; each must pass. Removing any one fails
closed.

```
  client ──DNS──▶ Traefik ──ipAllowList──▶ router ──▶ filebrowser ──OIDC──▶ Authentik
           (1)              (2)                          (3a login)   (3b caps/dirs)
```

1. **DNS reachability.** The hostname resolves to a mesh/edge IP. On the SGC box
   it points straight at the mesh IP (`169.254.0.127`) — unroutable off-mesh
   while DNS-01 still issues a real public cert. On prime it's public DNS to the
   EIP.
2. **Network (`ipAllowList`).** Traefik binds all interfaces, so DNS alone is not
   a boundary — a LAN or on-box client forcing the right SNI would otherwise
   reach the router. The router pins the allowed source range using the
   RemoteAddr strategy (no `forwardedHeaders` trust → unspoofable).
3. **Identity (OIDC).** Password auth is disabled, so there is no local
   credential. (3a) `userGroups` gates who may log in at all; (3b) once in,
   `groupPermissions`/`adminGroup`/`access.rules` decide capabilities and
   directory visibility.

**Design note — shared vs dedicated IdP determines the login gate.** A
*dedicated* Authentik (prime) already scopes its user pool to that realm, so its
app binding is the login gate and no `userGroups` is needed. A *shared* Authentik
(sgc — `auth.sgc.ai` fronts many realms) means every realm's users could
otherwise authenticate, so a `userGroups` login gate is **mandatory**. Any future
filebrowser on a shared IdP inherits this requirement.

---

## 7. Deployment topology

Two live deployments, same app and authority model, deliberately different
postures driven only by host vars.

| | **files.primebaseball.pro** | **files.sgc.ai** |
|---|---|---|
| Host | `primebaseball.pro` (dedicated t3.xlarge, prime-prod VPC) | alias on the SGC `prod` box (`169.254.0.127`) |
| Identity provider | **Dedicated** Authentik (`auth.primebaseball.pro`) | **Shared** Authentik (`auth.sgc.ai` → `pds-authentik-server`) |
| OIDC client | `filebrowser` | `sgc-files` |
| Groups | `prime-admins`, `prime-files-editors` | `sgc-files-admins`, `sgc-files-editors`, `sgc-files-users` |
| DNS gate | public DNS → EIP `98.95.5.180` | mesh DNS → `169.254.0.127` (unroutable off-mesh) |
| Network gate | ipAllowList: stargate mesh `169.254.41.0/24` + subnet `10.20.5.0/24` + facility IP `47.234.227.91/32` | ipAllowList: SSO mesh `169.254.0.0/24` |
| Login gate | — (dedicated IdP scopes the pool) | `userGroups: [sgc-files-users, editors, admins]` |
| East-west OIDC pin | `--add-host=auth.primebaseball.pro:10.20.5.176` | `--add-host=auth.sgc.ai:169.254.0.127` |
| App / unit / data | `1.5.0-2` · `prime-filebrowser-quantum` · `/prime/prime-filebrowser-quantum/` | `1.5.0-2` · `sgc-filebrowser-quantum` · `/sgc-files/sgc-filebrowser-quantum/` |

Both pin the issuer host to the local Traefik via `--add-host` because the box
can't usefully hairpin its own public issuer address for server-side OIDC calls;
browser redirects still use public DNS.

---

## 8. Build and release lifecycle

**Branches (3-branch SGC model).** `main` = upstream mirror (never edited
directly; `upstream` remote = `gtsteffaniak/filebrowser`). `sgc-dev` =
development. `sgc` = releases.

**Tags.** App releases on `sgc` carry the **bare** version string `X.Y.Z-N`
(upstream version + `-N`), which must equal `filebrowser_quantum_version` — the
role checks it out verbatim on self-build. Role releases carry `vX.Y.Z-N`
matching `requirements.yml`.

**Ship a new version to a host:**

```
  merge sgc-dev → sgc ; tag X.Y.Z-N ; push
  docker build --build-arg VERSION=<tag> --build-arg REVISION=<sha> \
    -t p3x-118/filebrowser:<tag> -f _docker/Dockerfile .        # on controller
  docker save p3x-118/filebrowser:<tag> | gzip \
    | ssh <box> 'gunzip | sudo docker load'
  # bump filebrowser_quantum_version in host vars, then:
  just install-service filebrowser-quantum --limit <host>        # from ~/sgc/SGC
```

The image is **controller-built and loaded**, never registry-pulled — so the
prod box compiles nothing and there is no external image dependency at deploy
time. The only non-P3X-118 build input is the `gtstef/ffmpeg:8.1.1-decode` base
image (Docker Hub, tag-pinned) referenced by `_docker/Dockerfile`; mirror/pin it
if full build independence is ever required.

---

## 9. Data and persistence

| State | Location (host) | In container | Store |
|---|---|---|---|
| App DB (users, shares, settings, **access rules**, API tokens) | `<base>/data/database.db` | `/home/filebrowser/data/database.db` | BoltDB |
| Rendered config | `<base>/data/config.yaml` | `/home/filebrowser/data/config.yaml` | file |
| File index | `<base>/cache/sql/index_all.db` | `/home/filebrowser/cache/sql/` | SQLite |
| The files themselves | `<base>/files` | `/folder` | filesystem |

Config-declared `access.rules` are applied over the persisted BoltDB rules at
each boot; the config authoritatively owns the paths it names. Everything else in
BoltDB (user records, shares) persists across restarts and redeploys — verified
by the `Using existing database` log line on every boot.

---

## 10. Operating notes

- **Grant a user file editing:** add them to `…-files-editors` (or `…-admins`)
  in Authentik. Effective at their next login. Nothing to do in the app.
- **Read-only staff (shared IdP):** add to `…-files-users` — passes the login
  gate, gets the read/download-only floor.
- **Directory boundaries:** declare `access.rules` in the host vars config
  extension (referenced groups auto-create); redeploy. None declared yet on
  either host (shares are empty).
- **Reach a mesh-gated host:** be on the SGC VPN mesh. On `files.sgc.ai` the DNS
  already points at the mesh IP, so no hosts-file edit is needed; on prime,
  public-DNS'd gated names must be resolved to the lighthouse locally.
- **Debugging a 403:** check which gate — Traefik access log
  (`journalctl -u <box>-traefik`) shows the rejected source IP for the network
  gate; the app log shows the `userGroups` rejection for the login gate.

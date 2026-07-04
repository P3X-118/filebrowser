# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

P3X-118's fork of **FileBrowser Quantum** (upstream `gtsteffaniak/filebrowser`): a single-binary web file manager — Go backend with the Vue 3 frontend embedded. This repo owns the full code lifecycle: track upstream, carry our patches, and **build Docker images locally** (upstream's CI/Docker Hub images are not relied on). The image is deployed by the sister ansible role `filebrowser_quantum` through the SGC playbook (see "Lifecycle" below).

Repo rules:

- `origin` = `git@github.com:P3X-118/filebrowser.git`; `upstream` = `https://github.com/gtsteffaniak/filebrowser.git` (fetch upstream, merge into `main` only).
- **Never rename the Go module path** (`github.com/gtsteffaniak/filebrowser/backend`) or import paths to P3X-118. `main` must remain a cleanly mergeable upstream mirror; P3X-118 alignment happens in git remotes, image names, and the ansible-role layer — not in source code.
- Branching follows the SGC 3-branch model (`~/sgc/CLAUDE.md`): `main` = upstream mirror (never edit directly), `sgc-dev` = our development, `sgc` = production releases. **Release tags on `sgc` are the bare version string** (`1.5.0-0` style: upstream version + `-N`, no `v` prefix) — the ansible role checks out `filebrowser_quantum_version` verbatim on self-build, so tag and role var must match exactly.
- Upstream's GitHub workflows (`.github/workflows/`: `beta/vX.Y.Z` branches → beta releases; `v*` tags → goreleaser + Docker Hub) are reference-only. Our builds are local; don't extend their CI.

## Commands

Prereqs: Go 1.25 (per `backend/go.mod`; an older local Go auto-fetches the toolchain), Node ≥ 22. Optional for media features: ffmpeg, exiftool, mupdf-tools. Run make targets from the repo root.

```bash
make setup            # first time: Go tooling, npm install, creates backend/test_config.yaml
make dev              # hot reload (vite watch + go air); uses backend/test_config.yaml
make run              # one-shot local run with PDF previews (mupdf build tag, needs CGO)
make build            # build-frontend + build-backend → backend/filebrowser (pure Go)
make generate-docs    # regen swagger docs + frontend/public/config.generated.yaml

make lint             # golangci-lint (backend) + eslint (frontend)
make test-backend     # cd backend && go test -race -timeout=10s ./...
make test-frontend    # vitest unit tests (colocated src/**/*.test.js)
make check-all        # lint + tests + i18n check — run before pushing
make test-playwright  # E2E suites, Docker-based (see Playwright notes below)

# Single tests
cd backend && go test -race ./http/... -run TestName
cd frontend && npx vitest run src/utils/url.test.js

# i18n — frontend/src/i18n/en.json is the master locale
make check-translations   # fails if any locale drifts from en.json (CI gate)
make sync-translations    # DeepL-translates missing keys (needs DEEPL_API_KEY)

# Docker images (local)
make build-docker         # full image (ffmpeg + mupdf), tags gtstef/filebrowser — retag for SGC use
make build-docker-slim    # minimal image, no ffmpeg/mupdf
```

## Architecture

**Single-binary pipeline:** `npm run build` writes to `backend/http/dist/` and copies to `backend/http/embed/`; Go embeds `embed/*` (`backend/http/httpRouter.go`). Both dirs are gitignored build artifacts. `frontend/public/index.html` is a **Go html/template** rendered by the backend (asset URLs via `{{ .htmlVars.staticURL }}`). With `FILEBROWSER_DEVMODE=true` the backend serves `http/dist` from disk instead of the embedded FS (how `make dev` hot-reloads). The Docker image copies dist to `./http/dist` rather than embedding.

**Backend** (`backend/`, module `github.com/gtsteffaniak/filebrowser/backend`):
- Boot: `main.go` → `cmd.StartFilebrowser()` (`cmd/root.go`). Config path: `-c` flag → `FILEBROWSER_CONFIG` env → `./config.yaml`. `settings.Initialize` loads, applies env overrides, and validates into the global `settings.Config` — struct definitions live in `common/settings/structs.go` and `common/settings/auth.go`; `backend/config.yaml` is the reference config.
- `http/` — stdlib `ServeMux` with Go 1.22 method patterns; middleware combinators (`withUser`, `withSelfOrAdmin`, rate limiting); all API handlers. Swagger from handler annotations (`make generate-docs`), served at `/swagger`.
- Auth: methods configured under `auth.methods.*` (password, proxy, oidc, ldap, jwt, passkey, noauth); the **first enabled method becomes the default login method**. Each user record stores a `LoginMethod`; cross-method logins are rejected (a 403 "wrong login method" means the user record's method doesn't match — change it, don't debug the IdP). Sessions are HS256 JWT cookies. **OIDC lives in `http/oidc.go`** (not `auth/`): ID-token verify with UserInfo-endpoint fallback (Authentik-friendly); `userIdentifier` claim → username; `groupsClaim` (default `groups`, dot-notation for nested claims) → groups; `adminGroup` membership grants admin and is **re-synced every login**; `userGroups` acts as a login allow-list; `createUser` auto-provisions; `store.Access.SyncUserGroups` reconciles IdP groups into the internal ACL groups on every login — path-level ACLs reference those groups.
- **Fork feature (≥ `1.5.0-1`, not upstream): `auth.methods.oidc.groupPermissions`** — a map of IdP group name → permission grants (`create/modify/delete/share/api/admin/realtime/download`). When set, every OIDC login recomputes the user's permissions as the `userDefaults` floor + the union of their groups' grants (grants AND revocations), so permission management lives entirely in the IdP. Without it, `userDefaults.account.permissions` (note: `account.` nesting — the top-level `userDefaults.permissions` is deprecated) stamp only at user creation. Implementation: `settings.PermissionsFromGroups` (`common/settings/settings.go`) + `http/oidc.go`.
- **Fork feature (≥ `1.5.0-2`, not upstream): top-level `access.rules`** — declarative path ACLs in config (`path`, optional `source`, `denyAll`, `allowUsers/allowGroups/denyUsers/denyGroups`), applied at startup by `cmd/access.go:applyConfigAccessRules`. The config owns the rules at the paths it declares (re-declare = replace, empty = clear); UI-created rules at other paths are untouched; referenced groups are auto-created before their first member logs in; a bad declaration fails startup (fail-closed). With `groupPermissions` + `access.rules` + IdP group sync, nothing access-related is managed in the filebrowser UI.
- SSO seamlessness: when password auth is disabled and a `?redirect=` is present, the login view auto-redirects to the OIDC flow (upstream behavior, `frontend/src/views/Login.vue`); RP-initiated logout via `auth.methods.oidc.logoutRedirectUrl` → Authentik end-session.
- Storage is two separate stores: **BoltDB** (storm) at the `server.database` path — users, shares, settings, access rules, API tokens; and a **SQLite file index** at `<server.cacheDir>/sql/index_all.db` (pure-Go driver by default; `cgosql` build tag switches to mattn/CGO).
- `indexing/` — per-source scanner goroutines on an adaptive schedule (no inotify); `events/` fans out SSE for realtime UI updates.
- Build tags: `mupdf` (CGO) enables PDF/office thumbnails — used by `make run` and the Docker image (`mupdf,musl`); plain `make build-backend` is pure Go with no C toolchain and no PDF previews.

**Frontend** (`frontend/`): Vue 3 + Vite 6, loose TypeScript (`strict: false`, mixed .js/.ts). State is a **hand-rolled reactive store** (`src/store/` — state/getters/mutations wired via provide/inject), not Pinia/Vuex. `vue-router@4`; `src/api/*` wraps native fetch (`api/utils.js` adds the sessionId header and auto-renews tokens). Prod builds gzip assets and delete the originals (`DEV_BUILD=true` disables this).

**Generated files — never hand-edit:** `frontend/public/config.generated.yaml` (generated from backend settings structs by `make generate-docs`), `backend/swagger/docs/`, `backend/http/dist|embed`.

**Playwright:** the root `frontend/playwright.config.ts` covers only the screenshot projects. Functional suites live in `frontend/tests/playwright/<suite>/` but use per-suite configs in `_docker/src/<suite>/frontend/playwright.config.ts`, running inside `_docker/Dockerfile.playwright-<suite>` containers (`make test-playwright`). The `oidc` suite runs against a mock OIDC server — useful when touching `http/oidc.go`.

## Lifecycle: app repo ↔ ansible role ↔ SGC playbook

| Piece | Location | State |
|---|---|---|
| App source + image builds | this repo | active |
| Sister role `filebrowser_quantum` | `~/sgc/ansible/roles/filebrowser-quantum-ar` (= `P3X-118/filebrowser-quantum-ar`, on `sgc-dev`) | released: `v1.0.3-2` on `sgc` |
| Master playbook wiring | `~/sgc/SGC` (`requirements.yml`, `setup.yml`, `group_vars/mash_servers`, `docs/services/filebrowser-quantum.md`) | ACTIVE — role fetched at `v1.0.3-2` |
| Live deployment | `files.primebaseball.pro` on host `primebaseball.pro` (host_vars there are the reference integration) | deployed `1.5.0-0` (image `p3x-118/filebrowser:1.5.0-0`, controller-built + docker-loaded) |

The role runs the image docker-in-systemd (MASH pattern, var prefix `filebrowser_quantum_`): read-only container, `--cap-drop=ALL`, `--user uid:gid`; mounts `<base>/cache → /home/filebrowser/cache`, `<base>/data → /home/filebrowser/data`, `<base>/files → /folder` (the single source); config rendered to `data/config.yaml`; **secrets (admin password, OIDC client secret, office JWT) go via the env file only, never the config file**. Config keys without dedicated role vars are set through `filebrowser_quantum_configuration_extension_yaml` (deep-merged into the rendered config). SGC's group_vars auto-wire Traefik labels and ONLYOFFICE integration when those services are enabled.

**Getting our image onto a host — two paths:**
1. **Role self-build** (the default marriage; role ≥ `v1.0.3-2`): set `filebrowser_quantum_container_image_self_build: true`. The role clones `https://github.com/P3X-118/filebrowser.git` on the target host at ref `filebrowser_quantum_version` (verbatim — hence the bare `X.Y.Z-N` tag convention), builds `_docker/Dockerfile` (path in `filebrowser_quantum_container_image_self_build_dockerfile`) with `VERSION`/`REVISION` build args, and runs it as `p3x-118/filebrowser:<ref>`.
2. **Prebuilt:** `make build-docker` here, retag `gtstef/filebrowser` to our name, push to a registry the host can reach, and override `filebrowser_quantum_container_image` in host vars.

**Remaining upstream references (deliberate):** the role's *pull-path* default stays `ghcr.io/gtsteffaniak/filebrowser` as a generic fallback (we deploy via self-build), and `_docker/Dockerfile` pulls the `gtstef/ffmpeg` base image from Docker Hub at build time. Role releases: tag on the role's `sgc` branch; the tag string must match the SGC `requirements.yml` `version:` exactly (`v1.0.3-2` style, `v`-prefixed — note this differs from this repo's bare app tags).

**Releasing a new version to the facility**: merge `sgc-dev` → `sgc`, tag bare `X.Y.Z-N`, push; `docker build --build-arg VERSION=<tag> --build-arg REVISION=<sha> -t p3x-118/filebrowser:<tag> -f _docker/Dockerfile .` on the controller; `docker save p3x-118/filebrowser:<tag> | gzip | ssh ubuntu@98.95.5.180 'gunzip | sudo docker load'`; bump `filebrowser_quantum_version` in the host vars; `just install-service filebrowser-quantum --limit primebaseball.pro` (from `~/sgc/SGC`).

## Deployment target: primeBaseball

This system is dedicated to the primeBaseball project (`~/b2b/primeBaseball`), deployed on the consolidated `primebaseball.pro` host (t3.xlarge in Market Reactor's dedicated AWS VPC, EIP 98.95.5.180 / private 10.20.5.176). **The complete, live integration is the `filebrowser_quantum` block in `~/sgc/SGC/inventory/host_vars/primebaseball.pro/vars.yml`** — treat it as the reference. Key facts:

- **Network gate**: public DNS → the EIP with a real DNS-01 LE cert, but the Traefik router carries an `ipallowlist` (`169.254.41.0/24` stargate mesh + `10.20.5.0/24` AWS subnet + facility IP `47.234.227.91/32`) as the sole network gate — everything else gets a 403 before the app. Same pattern as `do.` on that host.
- **Authentik is the IdP source of truth** — the dedicated `auth.primebaseball.pro` instance (not the shared SSO mesh). Login is OIDC-only (password method disabled), `client_id=filebrowser`, issuer `https://auth.primebaseball.pro/application/o/filebrowser/`. **Permissions are group-driven via the fork's `groupPermissions`** (matrix in the host vars): default = read/download-only; `prime-files-editors` and `prime-admins` → create/modify/delete/share; `prime-admins` → admin. Manage membership in Authentik only — changes apply at the user's next login. All Authentik group names also sync into filebrowser ACL groups (`SyncUserGroups`) for path-level rules. Do not create or manage local filebrowser users.
- The OIDC client secret derives from the host's `sgc_pgsk` (salt `filebrowser.oidc`) on BOTH sides: the host vars env var, and `FILEBROWSER_OIDC_SECRET` in `~/b2b/primeBaseball/.secrets/filebrowser-oidc.env` fed to the provisioner.
- **Authentik provisioning is scripted**: `~/sgc/apps/authentik/scripts/sgc/provision-filebrowser.py` (run via `run-provision.sh` conventions against `prime-authentik-server`; re-runnable, secret optional on re-runs). Redirect URI is fixed by the app: `https://files.primebaseball.pro/api/auth/oidc/callback`.
- Server-side OIDC calls pin `auth.primebaseball.pro` to the local Traefik via `--add-host=...:10.20.5.176` (the box can't hairpin its own EIP); browser redirects use public DNS.
- Facility files live at `/prime/prime-filebrowser-quantum/files` on the host, mounted at `/folder` in the container (the single filebrowser source).

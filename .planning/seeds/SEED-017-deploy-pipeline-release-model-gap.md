# SEED-017 — Deploy pipeline rot: Portainer webhooks don't force-pull + build-*-pod is develop-only → prod containers cannot receive fixes via the normal pipeline

**Discovered:** 2026-06-16 while pushing the CI fixes to prod (after SEED-015 unblock).
**Severity:** MED — once CI builds + pushes (SEED-015 fixed that), the new image still does not reach running containers. Both dev and prod ran stale images despite successful builds + fired webhooks. Manual `docker pull` + recreate is the only reliable path today.
**Related:** [[SEED-015-ci-deploy-blocked-primary-supervisord-integration-tests-red]] (the gate, now fixed), [[SEED-014]] (prod pod stuck on stale `:main` is a direct consequence). Memory: `ghcr-actions-install-broken`.

## Three independent propagation gaps

### 1. Portainer webhook fires but doesn't force-pull
`build-gateway` → `Trigger Portainer webhook (dev)` returns success, but the dev container (vps-ifix-vm) stayed on the OLD image revision (e.g. `3eea608`) while GHCR `:develop` had advanced (`2d22ec4`, verified by direct `docker pull`). The redeploy doesn't `--pull always`, so a cached local tag wins. Fix: enable "always pull image" / `pull_policy: always` on the Portainer stacks, or have the webhook redeploy force a pull.

### 2. GHCR_PAT half-fix (RESOLVED 2026-06-16)
The `GHCR_PAT` secret existed since 06-05 but the 3 build workflows still used `GITHUB_TOKEN` (broken org-wide: `permission_denied: The requested installation does not exist`). Fixed: swapped login password to `secrets.GHCR_PAT` in build-gateway/build-primary-pod/build-dashboard (commit `2d22ec4`). Image push now works (verified: `:develop` = `2d22ec4`).

### 3. build-primary-pod is develop-only → `:main` never rebuilt
`build-primary-pod.yml` triggers on **push to develop** only. The prod pod pulls `converseai-primary-pod:main`. So pod fixes merged anywhere never produce a new `:main` image — prod pod is permanently stale (newest GHCR pod image 06-08). This is the mechanism behind SEED-014 (prod pod missing the 06-13 chatterbox HF-offline fix). The gateway has the same shape (prod gateway tracks `:main`, develop builds `:develop`) but `build-gateway` DOES run on main pushes, so a develop→main merge rebuilds gateway `:main`; `build-primary-pod` does not.

## Net deploy model (as-built, fragile)

```
develop push → :develop built → DEV stack (Portainer GitOps, but no force-pull → stale)
main push    → :main built (gateway ONLY) → PROD (tracks :main, no force-pull → stale)
pod image    → :develop ONLY ever built; prod pod tracks :main → permanently stale
```

## Fix directions

1. **Force-pull:** set Portainer stacks (dev + prod) to always pull on redeploy; OR change the webhook/redeploy to `docker compose pull && up -d`.
2. **build-primary-pod on main:** add a `main` trigger (mirror build-gateway) so `:main` pod images get built; OR point the prod pod config at `:develop` if prod is meant to track dev (decide intentionally).
3. **Immediate prod pod fix (SEED-014):** manual PAT build+push of `converseai-primary-pod:main` from the 06-13 develop tip, + confirm MinIO chatterbox snapshot, then re-enable schedule.
4. **Verify a real deploy end-to-end** after fixing force-pull: push → build → pull lands in the container (assert the running revision matches the pushed commit).

# Contributing to jmap

Thanks for improving `jmap` and `jmapd`.

## Repository model

- Public mirror: <https://github.com/chatinfra/jmap.git>
- Canonical source: `go/jmap/` inside the ChatInfra monorepo

The public mirror exists for inspection, forks, local host edits, and pull requests. It is downstream of the monorepo. Maintainers import accepted public PRs into canonical `go/jmap` first, then synchronize the mirror.

Canonical `go/jmap` keeps the monorepo module path used by every module under `go/` (the `super/go/<tool>` form). The mirror sync rewrites published `*.go`, `go.mod`, and `*.md` module-path references so the public repository declares `module github.com/chatinfra/jmap` and public-facing examples use the mirror module path, for example:

```sh
go install github.com/chatinfra/jmap/cmd/jmap@latest
```

Maintainers apply the inverse transform when importing an accepted public PR, so canonical stays on the monorepo module path. Files outside `*.go`, `go.mod`, and `*.md` are published byte-for-byte without transform.

## Fork-and-PR flow

```sh
git clone git@github.com:<you>/jmap.git
cd jmap
git checkout -b my-jmap-change
go test ./...
git push -u origin my-jmap-change
```

Then open a pull request against `chatinfra/jmap`. Include any behavior, output-contract, or bridge-daemon implications in the PR description.

`jmap` command results are YAML and `--json` is unsupported, so a change that adds or alters structured output must also update the matching schema under `spec/outputs/` and keep `jmap schemas` consistent with the files that actually exist.

## Host-local edits

OpenCode hosts keep one editable mirror checkout at `/data/opencode/src/jmap`, shared by the `/data/opencode/bin/jmap` CLI launcher and the `/data/opencode/bin/jmapd` bridge-daemon launcher. Use this for diagnostics or emergency local patches:

```sh
sudo -u opencode git -C /data/opencode/src/jmap status
sudo -u opencode /data/opencode/bin/jmap --help
sudo -u opencode /data/opencode/bin/jmapd --help
```

Reconfigure preserves dirty host checkouts and logs a warning instead of resetting local work. To return to mirror updates, commit/stash/revert local edits, then re-run reconfigure so the clean checkout can fast-forward. Because `jmapd` runs as a supervised user-systemd service, restart the affected bridge unit after editing daemon source so the launcher rebuilds and the new binary is picked up.

## Maintainer import and mirror sync

Maintainers import accepted public changes into canonical `go/jmap`, preserving the monorepo as source of truth. For reviewed public mirror commits, generate an `mbox` patch and apply it with the monorepo helper so patch hunks are reverse-transformed back to the canonical module path:

```sh
git -C /path/to/chatinfra-jmap-mirror format-patch -1 --stdout <accepted-commit> > /path/to/pr.patch
bin/import_sched_public_pr --tool jmap /path/to/pr.patch /path/to/monorepo/go/jmap
```

`bin/import_sched_public_pr` lives in the monorepo next to the mirror sync tooling. It rewrites patch hunk content for `*.go`, `go.mod`, and `*.md`, refuses binary or non-allowlisted path-bearing patches, and then runs `git am` in the target canonical worktree. The same helper serves the companion CLI mirrors with `--tool sched`, `--tool specd`, `--tool xmpp`, or `--tool voice`.

For a one-off text-only patch that touches only `*.go`, `go.mod`, and `*.md`, the equivalent `git format-patch | sed | git am` flow is:

```sh
canonical_module='super/go'/'jmap'
public_module_regex='github[.]com/chatinfra'/'jmap'
git -C /path/to/chatinfra-jmap-mirror format-patch -1 --stdout <accepted-commit> \
  | sed -E "/^[ +-]/ s#${public_module_regex}#${canonical_module}#g" \
  | git -C /path/to/monorepo/go/jmap am -
```

Prefer the helper for normal imports; it validates patch shape before applying. After the monorepo change lands, run the mirror sync tooling from the monorepo root to update the public repository:

```sh
bin/sync_go_github --tool jmap
```

The sync treats `./go/jmap` as canonical source when run from the monorepo root, clones or reuses the public mirror checkout under `$SUPER_TMP_DIR/jmap-public-mirror-checkout` or `./tmp/jmap-public-mirror-checkout` via the SSH remote `git@github.com:chatinfra/jmap.git`, refuses dirty canonical or mirror state, requires mirror `HEAD` to match its fetched upstream exactly, copies only this module's subtree into the public mirror checkout, commits generated changes, and pushes the mirror branch. Use `--dry-run` to run the same preflight checks and prepare the transformed staging tree without touching the mirror checkout.

Verify a published result by cloning the public mirror over `https://` and running `go build -trimpath ./cmd/jmap` and `go build -trimpath ./cmd/jmapd` — both must build standalone, since OpenCode host launchers clone and build the mirror with no monorepo context.

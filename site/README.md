# Website

The public website: what Strife is, the in-browser playground, history
studio and verifier, downloads, documentation, compliance and pricing, in
Indonesian (default) and English. It is a static site, built with
[Hugo](https://gohugo.io/) and deployed to GitHub Pages by
`.github/workflows/pages.yml`.

## Build

Needs Go (the version in `go.mod`), Node 20+ and Hugo 0.148.2.

```sh
site/build.sh                  # everything, into site/public
OFFLINE=1 site/build.sh        # without asking GitHub for the latest release
HUGO=/path/to/hugo site/build.sh
```

`build.sh` runs the same steps as CI:

1. `wasm/cmd/sitedata` writes `data/`: the playground presets run with the
   native engine, the latest GitHub release, the release key (`release.pub`
   at the repository root, once published) and the compliance citations.
2. The engine is compiled to WebAssembly into `assets/wasm/`, then
   `wasm/smoke.cjs` checks in Node that it reproduces the native presets'
   hashes.
3. Hugo builds the pages; `scripts/check-wording.sh` fails the build if a
   phrase from the "Avoid" column of `docs/ROADMAP.md`, or a claim of
   regulatory approval, appears anywhere.

To preview, serve `public/` under the path in `baseURL`
(`/strife/` today).

## Layout

| Path | What |
| :--- | :--- |
| `content/id`, `content/en` | pages, one file per language (`translationKey` links them) |
| `content/*/docs/_content.gotmpl` | documentation generated from the repository's Markdown |
| `layouts/` | templates; `_markup/render-link.html` maps repository links to site pages |
| `i18n/` | interface strings |
| `assets/js/` | the playground, studio, verifier and their WebAssembly worker |
| `static/` | `install.sh`, `install.ps1`, sample histories, example reports |
| `wasm/` | a separate Go module: `bridge` adapts the engine for the browser |

## What the site reads from the engine

The site never copies the engine's text or numbers; it reads them at build
time, so a change in the engine shows up here (or fails the build):

- `README.md` chapters, `docs/BENCHMARKS.md`, `docs/github-action.md`,
  `examples/*/README.md` and `CHANGELOG.md` become pages. `docs/ROADMAP.md`
  stays internal.
- `pkg/explore`, `pkg/checker`, `pkg/report` and `pkg/attest` are compiled
  to WebAssembly through `wasm/bridge`, which adds no logic of its own.
- Release archives are expected as `strife_<tag>_<os>_<arch>.tar.gz|.zip`
  with `SHA256SUMS`, as `scripts/release/build.sh` writes them.
- `pkg/compliance.Citations` supplies the control IDs and citations.

## Settings

`hugo.toml`, `[params.contact]`: email, WhatsApp number, booking link,
optional form endpoint and security contact. Empty fields are hidden; the
contact form needs at least one of email, WhatsApp or form endpoint.

## Moving to a custom domain

1. In the repository's Settings → Pages, set the custom domain and follow
   GitHub's DNS instructions; enable "Enforce HTTPS".
2. Re-run the `pages` workflow. It builds with the address GitHub Pages
   reports, so links, the install commands and canonical URLs follow the
   new domain; nothing in the site needs to change.
3. Update the default `baseURL` in `hugo.toml` for local builds, and the
   link in the repository's `README.md`.

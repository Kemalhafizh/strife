# Situs Web

> **Bahasa:** [English](../../site/README.md) · Bahasa Indonesia

Situs web publik Strife menyajikan penjelasan produk, playground simulasi di browser, studio riwayat, verifikator bukti kriptografis, halaman unduhan, dokumentasi dwibahasa, pemetaan kepatuhan, dan harga dalam bahasa Indonesia (baku) serta bahasa Inggris. Situs ini merupakan situs statis yang dibangun menggunakan [Hugo](https://gohugo.io/) dan dideploy ke GitHub Pages melalui [`.github/workflows/pages.yml`](../../.github/workflows/pages.yml).

## Cara Membangun

Memerlukan Go (sesuai versi pada [`go.mod`](../../go.mod)), Node 20+, dan Hugo 0.148.2.

```sh
site/build.sh                  # Bangun seluruh situs ke site/public
OFFLINE=1 site/build.sh        # Tanpa menanyakan rilis terbaru ke API GitHub
HUGO=/path/to/hugo site/build.sh
```

`build.sh` mengeksekusi langkah yang sama seperti CI:

1. `wasm/cmd/sitedata` menulis `data/`: menjalankan preset playground menggunakan engine native, mengambil rilis GitHub terbaru, kunci rilis (`release.pub` di akar repositori setelah dipublikasikan), dan daftar kutipan kontrol kepatuhan.
2. Engine inti dikompilasi ke WebAssembly menjadi `assets/wasm/`, lalu `wasm/smoke.cjs` memverifikasi di Node bahwa modul Wasm menghasilkan hash yang persis sama dengan preset native.
3. Hugo membangun halaman statis; `scripts/check-links.cjs` memeriksa seluruh tautan dan `#anchor`, lalu `scripts/check-wording.sh` menggagalkan build bila ada frasa terlarang dari kolom "Avoid" di [`docs/ROADMAP.md`](../ROADMAP.md) maupun klaim persetujuan regulator yang muncul di berkas HTML mana pun.

Untuk pratinjau lokal, sajikan isi `site/public/` di bawah jalur yang sesuai dengan `baseURL` (`/strife/` saat ini).

## Struktur Direktori

| Jalur | Kegunaan |
| :--- | :--- |
| [`site/content/id`](../../site/content/id), [`site/content/en`](../../site/content/en) | Halaman statis per bahasa (ditautkan lewat `translationKey`) |
| `site/content/*/docs/_content.gotmpl` | Adapter konten yang membangkitkan halaman dokumentasi dari Markdown repositori |
| [`site/layouts/`](../../site/layouts) | Template Hugo; `_markup/render-link.html` memetakan tautan relatif repo ke URL halaman situs |
| [`site/i18n/`](../../site/i18n) | String antarmuka bahasa Indonesia (`id.toml`) dan Inggris (`en.toml`) |
| [`site/assets/js/`](../../site/assets/js) | Skrip interaktif playground, studio riwayat, verifikator, dan worker WebAssembly |
| [`site/static/`](../../site/static) | `install.sh`, `install.ps1`, contoh riwayat, serta contoh laporan interaktif |
| [`site/wasm/`](../../site/wasm) | Modul Go terpisah: `bridge` mengadaptasi engine inti untuk berjalan di browser |

## Data yang Dibaca Situs dari Engine

Situs web tidak pernah menyalin teks maupun angka tolok ukur secara manual; semuanya dibaca pada saat build sehingga setiap pembaruan engine otomatis tercermin (atau menggagalkan build bila tidak sinkron):

- **Dokumentasi bahasa Inggris** (`/en/docs/` & `/en/changelog/`): dibaca dari `README.md`, `docs/BENCHMARKS.md`, `docs/github-action.md`, `docs/server.md`, `examples/*/README.md`, dan `CHANGELOG.md`.
- **Dokumentasi bahasa Indonesia** (`/docs/` & `/rilis/`): dibaca dari `README.id.md`, `docs/id/BENCHMARKS.md`, `docs/id/github-action.md`, `docs/id/server.md`, `docs/id/examples/*/README.md`, dan `docs/id/CHANGELOG.md`.
- `docs/ROADMAP.md` dan `docs/id/ROADMAP.md` tetap bersifat internal. Bab pada `README.md` / `README.id.md` yang belum didaftarkan pada [`site/layouts/_partials/docs-adapter.html`](../../site/layouts/_partials/docs-adapter.html) tetap diterbitkan tepat setelah bab sebelumnya dengan peringatan build; daftarkan judulnya untuk menetapkan slug, grup, dan ringkasannya.
- Paket `pkg/explore`, `pkg/checker`, `pkg/report`, dan `pkg/attest` dikompilasi menjadi WebAssembly melalui `wasm/bridge` tanpa menambahkan logika verifikasi terpisah.
- Arsip rilis mengikuti pola `strife_<tag>_<os>_<arch>.tar.gz|.zip` disertai `SHA256SUMS` sebagaimana dihasilkan oleh `scripts/release/build.sh`.
- `pkg/compliance.Citations` memasok ID kontrol kepatuhan dan kutipan regulasinya.

## Pengaturan Kontak

Lihat [`site/hugo.toml`](../../site/hugo.toml), bagian `[params.contact]`: alamat email, nomor WhatsApp, tautan penjadwalan kalender, endpoint formulir opsional, dan kontak keamanan. Bidang kosong disembunyikan otomatis; formulir kontak memerlukan minimal salah satu dari email, WhatsApp, atau endpoint formulir.

## Beralih ke Domain Kustom

1. Di **Settings → Pages** repositori, atur domain kustom dan ikuti petunjuk DNS GitHub lalu aktifkan **"Enforce HTTPS"**.
2. Jalankan ulang workflow `pages`. Workflow membangun situs menggunakan URL yang dilaporkan GitHub Pages sehingga seluruh tautan, perintah instalasi, dan URL kanonik mengikuti domain baru secara otomatis.
3. Perbarui nilai default `baseURL` pada `site/hugo.toml` untuk build lokal beserta tautannya di `README.md` dan `README.id.md`.

# Tata Kelola Proyek

> **Bahasa:** [English](../../GOVERNANCE.md) · Bahasa Indonesia

**Strife** adalah perangkat uji sistem terdistribusi, verifikasi formal, dan penyusun bukti audit kode terbuka di bawah lisensi [Apache License 2.0](../../LICENSE). Dokumen ini menetapkan cara tata kelola repositori, proses pengambilan keputusan teknis, serta invarian yang wajib dijaga oleh setiap perubahan.

---

## Model Maintainer

Strife menggunakan model tata kelola **berbasis maintainer (*maintainer-led* / *"benevolent maintainer"*)** yang sesuai bagi sebuah engine verifikasi di mana kebenaran matematis dan determinisme eksekusi bergantung pada pengawasan arsitektur yang koheren.

### Peran

| Peran | Tanggung Jawab | Penugasan Saat Ini |
| :--- | :--- | :--- |
| **Pendiri & Lead Maintainer** | Arsitektur, persetujuan rilis, imbauan keamanan (*security advisories*), persetujuan determinisme/ketepatan (*soundness*), serta pembaruan tata kelola | [@Kemalhafizh](https://github.com/Kemalhafizh) (Muhammad Kemal Hafizh) |
| **Peninjau Bidang (*Area Reviewers*)** *(berkembang)* | Tinjauan kode dan triase pada subsistem spesifik (`pkg/checker`, `pkg/txn`, `pkg/client`, `pkg/agent`, `site`) yang diundang sesuai kontribusi berkelanjutan | Ditugaskan per tinjauan |
| **Kontributor** | Laporan isu, pereproduksi minimal `history.jsonl` / `--seed`, pengujian, korpus regresi fuzzing, perbaikan bug, dan dokumentasi | Komunitas |

Rute kepemilikan tinjauan ditegakkan melalui [`.github/CODEOWNERS`](../../.github/CODEOWNERS). Kontributor yang membangun kepemilikan berkelanjutan pada suatu subsistem dapat diundang menjadi co-maintainer atau peninjau bidang oleh Lead Maintainer.

---

## Pengambilan Keputusan Teknis

Sebagian besar perubahan—perbaikan bug, uji regresi, peningkatan dokumentasi, seed fuzzing, serta optimasi performa yang mempertahankan kontrak yang sudah ada—ditinjau langsung melalui *pull request* sesuai [`CONTRIBUTING.md`](CONTRIBUTING.md).

### Usulan yang Memerlukan Diskusi Desain Terlebih Dahulu

Bukalah *issue* yang menjelaskan motivasi, invarian, dan dampak kompatibilitasnya sebelum menulis implementasi besar untuk salah satu hal berikut:

1. **Skema Keluaran & Kontrak Eksternal**
   - Mengubah [`schemas/report.v1.json`](../../schemas/report.v1.json), [`schemas/history.v1.json`](../../schemas/history.v1.json), [`schemas/dossier.v1.json`](../../schemas/dossier.v1.json), [`schemas/campaign.v1.json`](../../schemas/campaign.v1.json), atau [`schemas/bundle.v1.json`](../../schemas/bundle.v1.json).
   - Mengubah kode keluar CLI (`0` lulus/cocok `--expect`, `1` pelanggaran/ketidakcocokan, `2` galat penggunaan/konfigurasi, `3` verdict tak konklusif `unknown`, `4` galat runtime/transport).
   - Mengubah ABI plugin beban kerja WebAssembly 1.0 (`next_op`) atau struktur predikat `in-toto` `https://strife.dev/attestation/v1`.
2. **Kebijakan Dependensi Inti**
   - [`go.mod`](../../go.mod) mempertahankan **nol dependensi eksternal** (murni pustaka standar Go). Usulan pengecualian pada sub-modul opsional wajib membuktikan isolasi dan pemisahan batas kompilasi (`site/wasm` adalah modul Go terpisah khusus untuk jembatan WebAssembly browser).
3. **Determinisme Simulator & Checker**
   - Mengubah `internal/rng`, `pkg/sim`, `pkg/nemesis`, atau `pkg/explore` dengan cara yang mengubah keluaran `trace_hash` / `schedule_hash` bagi seed yang sudah ada.
   - Mengubah batas ketepatan (*soundness*) pada `pkg/checker`, `pkg/txn`, `pkg/model`, atau `pkg/shrink` (`isSoundHistory` dan uji silang *brute-force*).
4. **Target Cluster Sungguhan, Transport & Kerangka Kepatuhan Baru**
   - Menambah klien protokol database (`pkg/client`), transport injeksi gangguan (`pkg/transport`, `pkg/agent`), atau pemetaan kontrol regulasi (`pkg/compliance`).

---

## Invarian Repositori yang Tidak Dapat Ditawar

Pull request akan ditolak apabila melanggar salah satu gerbang berikut yang diverifikasi oleh [`.github/workflows/ci.yml`](../../.github/workflows/ci.yml) dan [`.github/workflows/pages.yml`](../../.github/workflows/pages.yml):

1. **Nol Dependensi Eksternal di Inti:** [`go.mod`](../../go.mod) tidak memiliki blok `require`.
2. **Determinisme Simulator Bit Demi Bit:** penyapuan `--determinism` membuktikan eksekusi dan `trace_hash` identik di setiap pemutaran ulang untuk seed tetap.
3. **Verdict Jujur & Batas Bukti:** checker tidak pernah mengembalikan `linearizable` ketika anggaran pencarian habis (`unknown`, kode keluar `3`); dossier kepatuhan selalu menyatakan cakupan teruji vs tidak teruji dan tidak pernah mengklaim sertifikasi regulator.
4. **Klaim Dokumentasi Terukur:** klaim kuantitatif yang ditambahkan ke dokumentasi wajib cocok dengan eksekusi reproduksibel di [`BENCHMARKS.md`](BENCHMARKS.md) serta lolos [`site/scripts/check-wording.sh`](../../site/scripts/check-wording.sh) dan [`site/scripts/check-links.cjs`](../../site/scripts/check-links.cjs).
5. **Kompatibilitas Mundur Skema:** `go test ./schemas` (`TestCompatible`) menolak perubahan pemecah kompatibilitas pada skema v1.
6. **Validasi Cluster Sungguhan:** perubahan pada klien database langsung wajib bekerja terhadap target nyata (bukan sekadar server tiruan) dan menjaga seluruh asersi di [`examples/`](../../examples) + [`.github/workflows/live.yml`](../../.github/workflows/live.yml).

---

## Pembuatan Versi, Kebijakan Dukungan Jangka Panjang (LTS) v1.x, dan Rilis

Strife menganut [Semantic Versioning 2.0.0](https://semver.org/):

- **Masa Dukungan Jangka Panjang (LTS) & Akhir Dukungan (EOL) `v1.x`:**
  - Lini rilis `v1.x` (dimulai dari `v1.0.0`, dirilis November 2026) ditetapkan sebagai **Dukungan Jangka Panjang (LTS)** dan dipelihara aktif minimal selama **24 bulan** (setidaknya hingga **November 2028**).
  - Pemberitahuan awal minimal **6 bulan sebelumnya** akan dipublikasikan di [`CHANGELOG.md`](CHANGELOG.md), [`SECURITY.md`](SECURITY.md), dan [`README.id.md`](../../README.id.md) sebelum lini `v1.x` mencapai End-of-Life (EOL).
- **Kebijakan Backport & Tambalan (`v1.Y.Z`):**
  - Perbaikan keamanan, penguatan batasan panjang parser, perbaikan kebocoran transport, dan koreksi ketepatan (*soundness*) checker di-backport ke lini rilis aktif `v1.x` dan diterbitkan sebagai rilis tambalan (`v1.0.Z` / `v1.Y.Z`).
  - Sistem simulasi baru, flag CLI opsional, target database tambahan, serta bidang JSON opsional dapat dirilis dalam pembaruan minor (`v1.Y.0`) sepanjang menjaga kompatibilitas mundur penuh.
- **Kebijakan Perubahan Pemecah Kompatibilitas (Batas `v2.0.0`):**
  - **Tidak ada perubahan pemecah kompatibilitas (*breaking changes*)** yang diizinkan di dalam `v1.x` terhadap flag CLI terdokumentasi, kode keluar (`0`–`4`), endpoint HTTP v1 `strife serve` / `strife-agent` (`/api/v1/*`), ABI beban kerja WebAssembly 1.0 (`next_op`), ataupun kelima Skema JSON v1.0 beku (`schemas/report.v1.json`, `history.v1.json`, `dossier.v1.json`, `campaign.v1.json`, `bundle.v1.json`).
  - Di dalam `v1.x`, Skema JSON hanya boleh bertambah bidang opsional (ditegakkan otomatis oleh `TestCompatible` di [`schemas/schemas_test.go`](../../schemas/schemas_test.go)). Setiap perubahan tak kompatibel pada skema atau kontrak CLI mewajibkan kenaikan versi mayor (`v2.0.0`).
- **Penghapusan Fitur (Deprekasi):** setiap rencana penghentian flag CLI atau bidang API HTTP wajib tetap berfungsi penuh sepanjang `v1.x` dan diumumkan dalam [`CHANGELOG.md`](CHANGELOG.md) setidaknya satu rilis minor sebelum `v2.0.0`, kecuali pembatasan segera dibutuhkan untuk menanggulangi kerentanan keamanan kritis.
- **Pipeline Rilis ([`.github/workflows/release.yml`](../../.github/workflows/release.yml)):**
  - Dipicu dengan mendorong tag beranotasi `vX.Y.Z` yang cocok dengan entri `## [X.Y.Z]` di [`CHANGELOG.md`](CHANGELOG.md) dan konstanta `Version` di [`internal/version/version.go`](../../internal/version/version.go).
  - Menerbitkan biner statis (`linux`, `darwin`, `windows` pada `amd64`/`arm64`), `SHA256SUMS`, SBOM SPDX 2.3, dan image kontainer multi-arsitektur di `ghcr.io` secara otomatis, ditambah atestasi rilis `in-toto` v0.1 bertanda tangan Ed25519 (`strife sign --release`) apabila secret `STRIFE_RELEASE_KEY` terkonfigurasi.

---

## Ruang Lingkup Open-Core

Seluruh komponen di repositori ini—simulator deterministik, CLI `strife`, `strife-agent`, checker linearizability/transaksional/buku besar/kliring, klien protokol langsung, daemon registri run `strife serve`, worker jarak jauh `strife worker`, VM beban kerja WebAssembly, perkakas atestasi Ed25519, pembangkit dossier kepatuhan, dan situs web statis—berlisensi sumber terbuka [Apache License 2.0](../../LICENSE).

Apabila layanan SaaS terkelola opsional (seperti orkestrasi multi-cluster terkelola atau SLA dukungan komersial yang diuraikan pada [`ROADMAP.md`](ROADMAP.md)) ditawarkan secara terpisah, engine inti dan perangkat verifikasi di repositori ini tetap berlisensi Apache 2.0.

---

## Keamanan & Etika

- **Kerentanan Keamanan:** Ikuti prosedur pelaporan privat pada [`SECURITY.md`](SECURITY.md). Imbauan keamanan dikoordinasikan secara privat lalu diumumkan bersamaan dengan rilis tambalan.
- **Standar Komunitas:** Partisipasi dalam *issue*, *pull request*, dan tinjauan kode tunduk pada [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).
- **Saluran Bantuan:** Bantuan penggunaan, panduan reproduksi bug, dan tautan terkait tersedia di [`SUPPORT.md`](SUPPORT.md).

# Kebijakan Keamanan

> **Bahasa:** [English](../../SECURITY.md) · Bahasa Indonesia

Strife adalah proyek kode terbuka yang dibangun dengan rantai pasok minimal dan mudah diaudit: **nol dependensi eksternal Go** (`stdlib` murni), build deterministik, dan tanpa telemetri keluar.

## Versi yang Didukung & Pemeliharaan Keamanan LTS

Strife `v1.x` merupakan lini rilis aktif **Dukungan Jangka Panjang (LTS)**. Perbaikan keamanan serta tambalan ketepatan (*soundness*) diterapkan ke seri rilis `1.0.x` / `1.x` serta cabang `main` selama setidaknya **24 bulan** sejak rilis perdana `v1.0.0` (hingga minimal **November 2028**), dengan **pemberitahuan awal minimal 6 bulan** sebelum End-of-Life (EOL):

| Versi | Didukung | Komitmen Pemeliharaan |
| :--- | :--- | :--- |
| `1.0.x` (LTS) / `main` | **Ya** | Imbauan keamanan aktif, penguatan parser/transport, serta perbaikan *soundness* yang di-backport (setidaknya hingga November 2028) |
| `< 1.0.0` (`0.x`) | **Tidak** | Seri pengembangan pra-1.0; tingkatkan ke `v1.0.0` atau lebih baru |

## Cakupan & Batasan Model Ancaman

Kelas masalah berikut diperlakukan sebagai kerentanan keamanan:

1. **Autentikasi & Batasan Hak Akses `strife-agent` (`pkg/agent`, `cmd/strife-agent`)**
   - Penerobosan autentikasi token Bearer (`subtle.ConstantTimeCompare`), pembatasan pendengar non-loopback (selain bila `-insecure-no-token` diaktifkan eksplisit), validasi jalur/PID pada `/apply`, `/exec`, `/probes`, atau injeksi perintah melalui endpoint agen.
2. **Daemon HTTP Registri Run & Antrean Kerja `strife serve` (`pkg/serve`)**
   - Penerobosan pemeriksaan token Bearer pada `POST /api/v1/ingest`, `POST /api/v1/jobs`, `POST /api/v1/jobs/claim`, maupun `POST /api/v1/jobs/<id>/complete`.
   - Pengungkapan tidak sah muatan `live_config` pada permintaan `GET /api/v1/jobs` tanpa autentikasi, pemalsuan penyelesaian pekerjaan antar-worker, atau pemutaran ulang (*replay*) pekerjaan yang sudah selesai.
   - Alokasi memori tak terbatas melampaui batas tubuh permintaan (4 MiB untuk pembuatan pekerjaan, 32 MiB untuk pemasukan laporan/kampanye), injeksi HTML/skrip tersimpan (XSS) pada dasbor tersemat atau tampilan dossier kepatuhan, maupun kebocoran URL/token webhook melalui `/api/v1/webhooks` atau log.
   - *Catatan deployment:* `strife serve` berkomunikasi lewat HTTP teks polos di atas TCP (default `127.0.0.1:8080`). Saat mengekspos registri di luar loopback, akhiri TLS di depan `strife serve` dan biarkan `--token` aktif.
3. **Worker Jarak Jauh Berbasis Pull `strife worker` (`pkg/serve/worker.go`)**
   - Alokasi respons tak terbatas saat melakukan polling atau mengklaim tugas dari endpoint registri, atau kebocoran kredensial dari `live_config` ke dalam artefak laporan persisten (`runner.Config.Redacted()` wajib menyamarkan seluruh kredensial sebelum serialisasi laporan).
4. **Parser Masukan Tak Tepercaya (`pkg/client`, `pkg/history`, `pkg/workload`)**
   - Panic, pembacaan di luar batas (*out-of-bounds read*), loop tak berujung, atau kelelahan memori/stack tak terbatas saat memparsing bingkai protokol wire yang rusak (PostgreSQL, MySQL, RESP, Kafka, etcd), `history.jsonl`, riwayat Jepsen `.edn` (`maxEDNDepth`), OpenTelemetry Span JSON (`ExportTraceServiceRequest` di `pkg/history/otel.go`), maupun modul `.wasm`.
5. **Sandbox VM WebAssembly & Pelolosan Escaping Laporan (`pkg/workload`, `pkg/report`, `pkg/compliance`, `pkg/campaign`)**
   - Lolos dari mesin stack bermeter instruksi (`DefaultMaxFuel`), batas kedalaman tumpukan panggilan, atau batas memori linier selama eksekusi `.wasm` kustom.
   - Injeksi HTML/JS tanpa *escaping* ke dalam `report.html`, `campaign.html`, atau `dossier.html` yang bersumber dari bidang riwayat tak tepercaya.
6. **Atestasi Kriptografis & Verifikasi Rilis (`pkg/attest`, `pkg/compliance`)**
   - Penerobosan verifikasi tanda tangan Ed25519, kolisi/kelenturan (*malleability*) digest kanonik pada `report.json` / `bundle.json` / `dossier.json`, atau modifikasi isi yang tidak terdeteksi oleh `strife verify`.
7. **Redaksi Rahasia (`internal/secret`, `pkg/serve`)**
   - Kebocoran kredensial `env:NAMA` atau `file:PATH` yang telah diselesaikan, maupun token kueri webhook, ke dalam laporan, kampanye, atau artefak registri.

## Di Luar Cakupan

Perilaku berikut merupakan desain yang disengaja atau prasyarat operasional, bukan kerentanan keamanan:

- **Implementasi simulasi ber-bug:** Sistem seperti `raft-stale-reads`, `raft-lease`, `raft-unsafe-fsync`, `raft-ledger-overdraft`, `2pc-ledger-torn-commit`, dan `outbox-dual-write` memang sengaja menyuntikkan cacat protokol untuk keperluan uji verifikasi.
- **Prasyarat hak akses host untuk injeksi gangguan kernel:** Penyuntikan gangguan `iptables`, `tc netem`, atau `cgroup v2` pada cluster sungguhan secara inheren memerlukan hak root lokal, `CAP_NET_ADMIN`, atau akses host Docker pada node uji. Injeksi gangguan tanpa hak akses khusus host dapat memakai `transport.type: "proxy"`.
- **DoS lokal yang dipicu sendiri via flag CLI:** Memasukkan berkas lokal berukuran multi-gigabita secara langsung ke perintah CLI luring tanpa batas kepercayaan (*trust boundary*), kecuali disertai bug alokasi tak terbatas pada parser yang diuji.

## Melaporkan Kerentanan Keamanan

**Harap jangan membuka GitHub issue publik untuk dugaan kerentanan keamanan.**

Laporkan kerentanan secara privat melalui:

1. **GitHub Private Vulnerability Reporting (Disarankan):**
   Gunakan tombol [**Report a vulnerability**](https://github.com/Kemalhafizh/strife/security/advisories/new) pada tab **Security** di repositori ini.
2. **Email:**
   Hubungi maintainer langsung di `muhammadkemalhafizh@gmail.com` dengan subjek `[SECURITY] strife`.

Sertakan paket/subperintah terdampak, versi (`strife version`) atau hash commit, ringkasan dampak, serta pereproduksi minimal (baris perintah, `history.jsonl` / `.edn`, atau masukan pemicu fuzzing).

## Proses Tanggapan & Pengungkapan

- **Konfirmasi Penerimaan:** Maksimal **72 jam** setelah laporan diterima.
- **Penilaian & Triase:** Maksimal **7 hari**, disertai konfirmasi reproduksi dan tingkat keparahan.
- **Pengungkapan Terkoordinasi:** Perbaikan, uji regresi (atau kasus korpus fuzzing), serta catatan imbauan rilis dikoordinasikan bersama pelapor sebelum dipublikasikan secara terbuka. Masalah kritis dapat menerima rilis tambalan darurat (*out-of-band*).

## Memverifikasi Integritas Rilis

Artefak rilis menyediakan dua lapis verifikasi di [`.github/workflows/release.yml`](../../.github/workflows/release.yml):

1. **Checksum & SBOM SPDX (Selalu Tersedia di Setiap Rilis):**
   Setiap GitHub Release tanpa syarat menyertakan `SHA256SUMS` yang mencakup seluruh arsip platform (`linux`, `darwin`, `windows` pada `amd64`/`arm64`) beserta Software Bill of Materials SPDX 2.3 terbaca mesin (`strife_<version>.spdx.json`).
2. **Atestasi Kriptografis Ed25519 `in-toto` (Bila Kunci Penandatangan Terkonfigurasi):**
   Ketika secret `STRIFE_RELEASE_KEY` terkonfigurasi di environment rilis, workflow menandatangani seluruh arsip, `SHA256SUMS`, dan SBOM SPDX menjadi bundel Pernyataan `in-toto` v0.1 (`strife_<version>.intoto.json`) yang dapat diverifikasi dengan `release.pub`.

```sh
# 1. Verifikasi digest SHA-256 arsip (tersedia di semua rilis)
sha256sum -c --ignore-missing SHA256SUMS

# 2. Verifikasi bundel asal-usul kriptografis Ed25519 in-toto (bila strife_<version>.intoto.json terlampir)
strife verify --bundle strife_<version>.intoto.json --key release.pub --release-dir ./downloaded-release
```

Verifikasi sisi klien langsung di browser juga tersedia melalui [https://kemalhafizh.github.io/strife/verifikasi/](https://kemalhafizh.github.io/strife/verifikasi/) dan berjalan sepenuhnya di WebAssembly tanpa pengunggahan data maupun telemetri.

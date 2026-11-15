# Catatan Rilis

> **Bahasa:** [English](../../CHANGELOG.md) · Bahasa Indonesia

Seluruh perubahan penting pada Strife. Format mengikuti
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) dan proyek ini menganut
[Semantic Versioning](https://semver.org/). Keluaran JSON di dalam
[`schemas/`](../../schemas/) merupakan kontrak berversi: di dalam satu versi mayor,
skema hanya boleh bertambah bidang opsional.

## [Unreleased]

### Diperbaiki

- **Verdict jujur untuk checker transaksional *sound-incomplete* (`pkg/model`, `pkg/checker`, `pkg/compliance`):**
  - Mengganti keluaran biner `model.Decider` menjadi `DecisionStatus` tiga keadaan (`DecisionViolation`, `DecisionVerified`, `DecisionInconclusive`).
  - Model *sound-incomplete* (`append`, `ledger`, `settlement`) kini mengembalikan `DecisionInconclusive` saat tidak menemukan anomali (riwayat kosong tetap `DecisionVerified`), tidak langsung mengklaim pembuktian penuh `linearizable`.
  - Menambahkan pencarian WGL tuntas berbatas (`<= 16` operasi, `<= 4` operasi `info`, `<= 500.000` langkah) melalui `model.CompleteSearcher` untuk riwayat `strict-serializable` yang memenuhi syarat, dan mengembalikan `Verdict: Unknown` dengan `Reason: "sound incomplete"` saat batas terlampaui.
  - Menambahkan `--expect no-anomaly` pada `strife sim`, `strife campaign`, `strife check`, `strife run`, laporan, workflow CI, dan jembatan Wasm, serta mempertahankan `ReasonIncomplete` di `schemas/campaign.v1.json`.
  - Menilai kontrol kepatuhan yang didukung verdict *sound-incomplete* sebagai `PARTIAL` (disertai pengungkapan eksplisit di **Metodologi & Batasan Cakupan**) alih-alih `PASS` (`FAIL` tetap berlaku pada pelanggaran atau habisnya anggaran).
- **Verifikasi kesetaraan optimasi WGL (`pkg/checker`):**
  - Menambahkan `TestDifferentialOptimizerEquivalence` yang membandingkan solver teroptimasi baku, `SearchOnly`, penonaktifan `eagerRead`, penonaktifan `kvPruner`, dan WGL tanpa optimasi terhadap oracle *brute-force* pada riwayat acak.
- **Autentikasi multi-tenant dan pemulihan lease antrean pada `strife serve` (`pkg/serve`):**
  - Menegakkan autentikasi Bearer *fail-closed* (`--token` admin dan `--tenant-tokens tenant=rahasia`) di dasbor tersemat (`/`) dan titik akhir `/api/v1/*`, sambil mempertahankan `GET /api/v1/keys` publik dan menyunting detail `GET /api/v1/health` bila tanpa autentikasi.
  - Membatasi `GET /api/v1/webhooks` hanya untuk token admin (`403` bagi token tenant) dan menegakkan isolasi tenant `403` secara ketat pada run, dossier, deret waktu, tren, dan antrean pekerjaan.
  - Menambahkan `claim_token` pekerja, `--job-lease` terkonfigurasi (default `2m`), `POST /api/v1/jobs/<id>/heartbeat`, heartbeat otomatis pada `strife worker`, pengambilan ulang pekerjaan saat lease kedaluwarsa, penjaga konkurensi `completing` (`409 Conflict`), serta penyimpanan atomik untuk `jobs.json` dan `campaign.json`.

## [1.0.0] - 2026-11-14

### Ditambahkan (1.0.0)

- Kerangka kepatuhan regulasi keuangan pada `pkg/compliance` dan `strife compliance`:
  - **PCI-DSS v4.0 (`pci-dss` / `pcidss`)**: mengevaluasi `pcidss.data-integrity` (Req. 6.2.4 / 12.10.5), `pcidss.resilience-change-verification` (Req. 6.5.2 / 11.4), dan `pcidss.tamper-evidence` (Req. 10.3.2 / 10.3.4).
  - **Bank Indonesia Sistem Pembayaran (`bi-sp` / `pbi-sp` — PBI No. 23/6/PBI/2021 & PBI No. 23/7/PBI/2021)**: mengevaluasi `bi-sp.keandalan-sistem`, `bi-sp.pencegahan-duplikasi`, `bi-sp.integritas-bukti-audit`, dan finalitas penyelesaian transaksi keuangan `bi-sp.finalitas-settlement`.
- Kontrak Skema JSON v1.0 LTS beku untuk `schemas/campaign.v1.json` (`campaign.Manifest`) dan `schemas/bundle.v1.json` (`attest.Bundle`) berdampingan dengan `report.v1.json`, `history.v1.json`, dan `dossier.v1.json`.
- Target fuzzing berkelanjutan (`FuzzReadEDN`, `FuzzReadOTel`, dan perluasan `FuzzReadJSONL`) di `pkg/history/fuzz_test.go`.

### Diperbaiki (1.0.0)

- Penguatan ketepatan (*soundness*) checker, batasan panjang masukan (dekoder protokol jaringan, parser JSONL/EDN, serta pemuat modul Wasm), penjagaan klasifikasi hasil klien langsung, dan penanganan gangguan cluster langsung ([#13](https://github.com/Kemalhafizh/strife/pull/13)).

### Dokumentasi & Tata Kelola (1.0.0)

- Penambahan tata kelola proyek (`GOVERNANCE.md`), `NOTICE`, `.github/CODEOWNERS`, konfigurasi Dependabot, perluasan `SECURITY.md`, pembaruan alur verifikasi kontributor (`CONTRIBUTING.md`), serta penyelarasan `README.md`, `docs/`, dan `examples/`.

## [0.8.0] - 2026-11-14

### Ditambahkan (0.8.0)

- Worker jarak jauh berbasis *pull* (`strife worker`) serta API antrean pekerjaan (`POST /api/v1/jobs`, `POST /api/v1/jobs/claim`, `POST /api/v1/jobs/{id}/complete`, `GET /api/v1/jobs`) di `strife serve`, memungkinkan VPC privat dan cluster staging terisolasi mengeksekusi simulasi, kampanye multi-seed, dan uji database langsung tanpa membuka port masuk jaringan.
- Importir jejak OpenTelemetry Span JSON tanpa dependensi eksternal (`pkg/history/otel.go`), dikenali otomatis oleh `strife check` di samping riwayat Strife JSONL dan Jepsen EDN.
- Gerbang SLO ketahanan kuantitatif (`--max-mttr`, `--min-availability`) pada `strife sim`, `strife check`, `strife run`, dan `strife ci-report`, ditambah pelacakan tren ketahanan deret waktu (`GET /api/v1/trends`) di `strife serve`.
- Generator ringkasan komparatif Markdown dan gerbang Pull Request CI bawaan (`strife ci-report`) yang membandingkan verdict konsistensi, delta MTTR/ketersediaan, regresi kampanye, dan kontrol kepatuhan regulasi.

## [0.7.0] - 2026-11-07

### Ditambahkan (0.7.0)

- Kunci penandatanganan Ed25519 per tenant (`--tenant-keys`), penemuan kunci publik
  (`GET /api/v1/keys`), serta dasbor ringkasan SLA deret waktu multi-tenant di
  `strife serve` (`pkg/serve`):
  - `--tenant-keys tenantA=keyA.key,tenantB=keyB.key` menandatangani laporan
    masuk dengan kunci Ed25519 khusus tenant (`strife-serve:<tenant>`) lalu
    memverifikasinya otomatis pada `GET /api/v1/runs/<id>/dossier`.
  - `GET /api/v1/keys` menyajikan kunci verifikasi publik Ed25519 aktif dalam
    format PEM PKIX (`public_key_pem`, `key_id`, `tenant`, `signer`).
  - Dasbor web tersemat (`GET /`) kini menampilkan tabel `Target & Tenant
    Time-Series SLA Overview` dan mendukung penyaringan `?tenant=`.
- Gerbang kontrol regulasi SLA/MTTR kuantitatif (`--max-mttr`,
  `--min-availability`) serta keluaran SARIF 2.1.0 (`STRIFE-COMPLIANCE-*`) di
  `strife compliance` (`pkg/compliance`, `pkg/report/sarif.go`).
- Agregasi ketersediaan dan pemulihan pasca-gangguan tingkat kampanye
  (`avg_availability`, `mttr_p50_ms`, `mttr_p95_ms`, `mttr_max_ms`) serta
  diagnosis pelanggaran multi-model (`ledger`, `settlement`) di `strife campaign`
  (`pkg/campaign`).
- Dukungan input `sarif`, `push`, `push-token`, `tenant` dan output `sarif` pada
  GitHub Action (`action.yml`, `scripts/github-action.sh`).

## [0.6.0] - 2026-11-07

### Ditambahkan (0.6.0)

- Isolasi registri multi-tenant, analitik SLA deret waktu, serta klien push
  bawaan di `strife serve` (`pkg/serve`):
  - Isolasi baseline kampanye per tenant (bidang `tenant` / parameter kueri
    `?tenant=` pada `/api/v1/ingest`, `/api/v1/runs`, dan `/api/v1/series`).
  - `GET /api/v1/series` mengagregasi metrik deret waktu per `(tenant, target)`
    (`pass_rate`, `avg_availability`, `latest_mttr_p95_ms`, `max_mttr_p95_ms`,
    serta `points` kronologis).
  - `serve.Push` serta flag `--push` / `--push-token` / `--tenant` pada
    `strife check` mengirim laporan langsung ke registri `strife serve`.
- Keluaran SARIF 2.1.0 (`--sarif <file>` di `pkg/report/sarif.go`): memetakan
  pelanggaran linearizability, transaksional (`G0`, `G1a/b/c`, `G2`, `G2-item`,
  `G-realtime`), buku besar berpasangan (`LEDGER-*`), kliring (`SETTLEMENT-*`),
  dan regresi kampanye ke aturan SARIF terstruktur untuk GitHub Code Scanning.
- Interoperabilitas dua arah Jepsen EDN (`history.WriteEDN` di
  `pkg/history/edn.go` dan `strife check --export-edn <file>`): mengekspor
  riwayat Strife (termasuk transaksi `kv`, `append`, `ledger`, dan `settlement`)
  tanpa kehilangan informasi ke format kanonik `.edn` Jepsen.
- Bagian interaktif `Resilience & Post-Fault Recovery (MTTR)` di `report.html`
  (`pkg/report/html.go`): menampilkan ketersediaan menyeluruh, jendela sehat,
  dan jendela gangguan bersama P50/P95/Max MTTR serta timeline pemulihan tiap
  gangguan.

## [0.5.0] - 2026-11-07

### Ditambahkan (0.5.0)

- Rekonsiliasi kliring cluster langsung & transaksional outbox (`workload.model:
  "settlement"` di `strife run` dengan audit final atomik `finalSettlementAudit`,
  append kliring idempoten `MemoryStore`, serta
  `examples/postgres/strife-settlement.json`).
- Pengecilan riwayat transaksional yang sah (`strife shrink --history` di
  `pkg/shrink/history.go`): mempertahankan keutuhan mikro-operasi produser-konsumen
  di seluruh riwayat `txn`, `append`, `ledger`, dan `settlement` (JSONL dan
  Jepsen EDN) serta memangkas transaksi audit hanya-baca di fase `pure-reads`.
- Injeksi gangguan *half-duplex* / putus respons setelah permintaan tiba di `pkg/transport`
  (`Proxy.DropResponseAfterRequest` dan `Proxy.ForwardedBytes`) untuk memicu
  status klien tak pasti (`info`) secara deterministik sesudah operasi tereksekusi
  di server hulu.
- Pembuatan dossier kepatuhan sesuai permintaan & peringatan pelanggaran SLA di
  `strife serve`:
  - `GET /api/v1/runs/<id>/dossier` (mendukung `?format=html` dan
    `?frameworks=dora,soc2,iso27001,bi-ojk`) mengevaluasi dossier kepatuhan
    langsung dari run tersimpan serta otomatis memverifikasi bundel tanda tangan
    Ed25519 `in-toto` yang tersimpan.
  - Flag `--max-mttr` dan `--min-availability` di `strife serve` mengirim
    peringatan webhook `report.sla_breach` ketika latensi pemulihan pasca-gangguan
    (MTTR P95) atau ketersediaan layanan melanggar ambang SLA.
  - `RunEntry` di `registry.jsonl` kini mengindeks `overall_availability`,
    `fault_window_availability`, `mttr_p50_ms`, `mttr_p95_ms`, dan `mttr_max_ms`.
- Target fuzzing `FuzzReadEDN` di `pkg/history` serta dukungan Studio WebAssembly
  browser untuk riwayat Jepsen `.edn` dan preset `outbox-dual-write`.

## [0.4.0] - 2026-11-07

### Ditambahkan (0.4.0)

- Proxy injeksi gangguan L4 TCP ruang pengguna (`transport.type: "proxy"` di
  `pkg/transport/proxy.go`): menyuntikkan `partition`, `latency`, `loss`, `kill`,
  `pause`, `freeze`, dan `io-stall` lintas Linux, macOS, dan Windows tanpa hak
  root, `iptables`, `tc netem`, maupun `cgroup v2`.
- Perintah CLI `strife doctor` (`pkg/transport/doctor.go`): memeriksa OS host,
  hak akses root, `cgroup v2`, perkakas CLI lokal (`docker`, `kubectl`, `ssh`,
  `tc`, `iptables`, `proxy-l4`), serta keterjangkauan TCP ke endpoint cluster.
- Metrik ketahanan kuantitatif & latensi pemulihan pasca-gangguan (`MTTR`) pada
  `report.json`, `report.md`, dan dossier kepatuhan (`overall_availability`,
  `healthy_window_availability`, `fault_window_availability`, `mttr` P50/P95/Max,
  dan daftar pemulihan per gangguan `recoveries`).
- Model konsistensi kliring lintas-sistem & transaksional outbox
  (`--model settlement`, sistem simulasi `outbox-settlement` dan
  `outbox-dual-write`): membuktikan `SETTLEMENT-ATOMICITY`,
  `SETTLEMENT-NO-ORPHAN-CREDIT`, dan `SETTLEMENT-IDEMPOTENCY`.
- Parser riwayat `.edn` Jepsen native (`pkg/history/edn.go`) dengan deteksi
  format otomatis di `history.Read` agar `strife check` dan `strife shrink`
  langsung menerima log EDN ekspor Jepsen.

## [0.3.0] - 2026-11-07

### Ditambahkan (0.3.0)

- Audit buku besar berpasangan pada cluster sungguhan (`workload.model: "ledger"` di
  `strife run`): menjalankan transfer antar-akun konkuren (`acct:i -> acct:j`)
  dan audit saldo total di PostgreSQL (`SELECT ... FOR UPDATE` / `txn_isolation`
  terkonfigurasi), MySQL/TiDB, etcd v3 (transaksi optimistik `mod_revision`),
  dan Redis/Valkey (skrip Lua atomik), diakhiri audit saldo akhir pasca-pemulihan
  (`examples/postgres/strife-ledger.json`).
- Sistem simulasi buku besar *two-phase commit* (`2pc-ledger` dan
  `2pc-ledger-torn-commit`): memodelkan koordinasi *prepare/commit* multi-fase
  serta menangkap bug koordinator Saga / 2PC non-atomik di mana kaki debit masuk
  sebelum kaki kredit (`LEDGER-CONSERVATION` saat `partition` dan `kill`).
- Peringatan webhook di `strife serve` (`--webhook <urls>` dan `GET
  /api/v1/webhooks`): mengirim notifikasi Slack, Discord, atau JSON generik saat
  laporan masuk melanggar konsistensi (`report.violation`) atau kampanye mengandung
  pelanggaran maupun regresi seed (`campaign.regression` / `campaign.violation`).

### Diubah (0.3.0)

- Pembangunan Direct Serialization Graph (`pkg/txn`) kini memakai deduplikasi
  sisi per simpul $O(1)$ dan reduksi transitif urutan interval untuk sisi waktu
  nyata (`ro`) dan tulis-tulis (`ww`), mempercepat pemeriksaan ledger dan transaksi
  strict-serializable besar ~10x lipat.
- `kvPruner` di `pkg/checker` menahan kandidat penulisan tak pasti (`info`) di
  belakang kandidat pasti (`ok`) pada urutan pencarian linearizability Wing & Gong
  / Lowe agar riwayat kekacauan multi-klien tuntas tanpa kehabisan anggaran langkah.

## [0.2.0]

Fase 5 dari [peta jalan](ROADMAP.md): setiap klaim yang dipublikasikan kini
memiliki job CI yang mereproduksinya, seluruh klien berjalan terhadap versi
database terkini beserta keamanan bawaannya, dan Strife dirilis sebagai paket
bertanda tangan.

### Ditambahkan (0.2.0)

- `strife bench inject`: mengukur keterlambatan pendaratan gangguan pada cluster
  nyata per transport dan jenis gangguan, serta waktu di dalam agen; CI langsung
  mempublikasikan angkanya yang terhimpun di
  [`docs/id/BENCHMARKS.md`](BENCHMARKS.md).
- `strife shrink --live`: *delta debugging* jadwal gangguan uji langsung di
  cluster nyata, beberapa percobaan per kandidat, dilaporkan sebagai pereproduksi
  kandidat beserta tingkat reproduksi terukur. `strife run --schedule` memutar
  ulang jadwal terekam atau hasil pengecilan.
- Transport Kubernetes (`"type": "kubernetes"`): menyuntikkan gangguan melalui
  `kubectl exec`, `"ip": "auto"` diselesaikan menjadi IP pod, endpoint `{ip}`
  diperluas per node; `examples/kubernetes` dan job kind di CI.
- Transaksi list-append yang diperiksa bergaya Elle (`--model append`, langsung
  `workload.model: append`): G0, G1a/b/c, G-single, G2, G-realtime, append hilang,
  urutan tak cocok, pembacaan phantom, duplikat, dan internal. Uji append langsung
  diakhiri pembacaan final atas seluruh kunci.
- Klien: PostgreSQL SCRAM-SHA-256/MD5 dan TLS; MySQL/TiDB `caching_sha2_password`
  (jalur cepat dan autentikasi penuh) dan TLS; Kafka dengan ApiVersions, routing
  leader, dan RecordBatch v2 (3.x dan 4.x); Redis AUTH dan TLS; transaksi
  list-append untuk PostgreSQL, MySQL/TiDB, Redis, etcd, dan Kafka. `db.password`
  dan `transport.token` menerima `env:NAMA` dan `file:PATH`, sementara rahasia
  literal disamarkan dari laporan.
- `db.txn_isolation` dan `db.init_sql` untuk target SQL.
- Contoh tervalidasi di CI: PostgreSQL 17, MySQL 8.4, TiDB 8.5, Kafka 3.9 dan
  4.1, transaksi etcd, etcd di Kubernetes, gangguan cgroup v2, dan `strife-agent`.
  Konfigurasi lemah yang dikenal wajib tertangkap: *write skew* dan pembacaan
  standby basi pada PostgreSQL, serta `REPEATABLE READ` pada MySQL dan
  TiDB-pesimistis.
- Kontrol kepatuhan menyertakan `citation` ketentuan regulasi yang didukungnya.
- Transport Docker: `sudo` untuk perintah host, penemuan otomatis `"cgroup":
  "auto"` dan `"io_device": "auto"`.
- Pipeline rilis: arsip Linux, macOS, Windows, `SHA256SUMS`, SBOM SPDX, atestasi
  rilis `in-toto` yang ditandatangani dengan `strife sign --release`, serta
  image `ghcr.io/kemalhafizh/strife` dan `strife-agent`. GitHub Action mengunduh
  biner terverifikasi sesuai tag-nya.
- Skema JSON untuk `report.json`, `history.jsonl`, dan `dossier.json`, disertai
  pengujian yang menolak perubahan yang memecah kompatibilitas.
- Fuzzing seluruh parser byte yang tak tepercaya; uji silang *brute-force* kedua
  checker transaksional termasuk pengukuran kelengkapan list-append.
- Buku besar keuangan berpasangan dan pemeriksa konservasi uang (`--model ledger`,
  sistem simulasi `raft-ledger` dan `raft-ledger-overdraft`): membuktikan
  `LEDGER-CONSERVATION` ($\sum B_i = M_0$), `LEDGER-OVERDRAFT` ($B_i < 0$ dan
  *write skew* `G2-item`), `LEDGER-DOUBLE-SPEND` pada pengulangan timeout `info`,
  serta `LEDGER-UNBALANCED` ($\sum \Delta_k \ne 0$), terintegrasi dengan kontrol
  kepatuhan `bi-ojk.rekonsiliasi-buku-besar`.
- Pemangkasan interval nilai (`kvPruner`) pada solver linearizability Wing & Gong
  / Lowe: merambatkan jendela efektif transitif `[effCall, effReturn]` melintasi
  penulisan ber-pembuat tunggal dan transisi CAS, menuntaskan 10.000 operasi di
  50 proses konkuren dalam 22.913 langkah (~534 ms).
- Pemartisi policy routing FIB Linux `AF_NETLINK` native (`RTM_NEWRULE` /
  `RTM_DELRULE` dengan `FR_ACT_BLACKHOLE`) di `strife-agent`, dengan fallback
  otomatis ke `iptables`.
- Penemuan cgroup v2 host Kubernetes dan namespace jaringan pod `/proc/<pid>/ns/net`
  di `strife-agent` (`pod:<ns>/<name>`, `k8s:<name>`, `container:<id>`) beserta
  manifes DaemonSet (`deploy/kubernetes/strife-agent-daemonset.yaml`).
- `strife serve` ([`docs/id/server.md`](server.md)): daemon HTTP verifikasi
  berkelanjutan & registri run self-hosted dengan penyimpanan artefak
  *append-only*, REST API (`/api/v1/ingest`, `/api/v1/runs`, `/api/v1/health`),
  perbandingan regresi baseline seed otomatis, penandatanganan Ed25519 `in-toto`
  opsional saat ingest, serta antarmuka web tersemat.
- Situs web klien dwibahasa (`site/`, bahasa Indonesia dan Inggris) dengan
  playground WebAssembly di browser, studio riwayat, dan verifikator atestasi
  kriptografis.

### Diubah (0.2.0)

- ID kontrol kepatuhan kini berupa label Strife (`dora.data-integrity`,
  `soc2.recovery`, `bi-ojk.jejak-audit`…) alih-alih string yang menyerupai nomor
  pasal resmi. Skrip yang mencocokkan ID lama harus diperbarui.
- `strife-agent` menolak mendengarkan di alamat jaringan tanpa token autentikasi.
- Eksekusi mencatat saat setiap gangguan benar-benar mendarat (setiap node
  mengonfirmasi), bukan hanya saat pengiriman dimulai; laporan merangkum pewaktuan
  gangguan.
- Pembekuan (`freeze`) hanya dilaporkan berhasil setelah `cgroup.events` menandai
  cgroup berstatus frozen.
- Eksekusi langsung tanpa satu pun operasi yang berhasil berstatus `unknown`,
  bukan lulus.
- Pemeriksa transaksi register hanya menarik urutan versi dari waktu nyata pada
  `strict-serializable`, dan membaca hasil dari penyelesaian operasi.

### Diperbaiki (0.2.0)

- Statistik "jitter" agen sebelumnya merupakan stempel waktu Unix bukan durasi,
  dan angka 0,28 ms yang pernah dipublikasikan tidak memiliki tolok ukur; kini
  digantikan pengukuran nyata.
- `strife-agent` mengodekan parameter netem dalam urutan bidang dan satuan yang
  keliru sehingga gangguan `loss` mengalami timeout.
- Kontrol integritas data DORA sebelumnya mengutip "Art. 9.4(c)"; ketentuan yang
  tepat adalah Art. 9(3)(b)–(c).
- Standby PostgreSQL di dalam contoh sebelumnya gagal mengkloning primary saat
  checkpoint mendaur ulang WAL di tengah penyalinan.
- Transaksi PostgreSQL mengaitkan baris ke pembacaan yang salah setelah
  `SELECT` kosong.
- Level isolasi beban kerja sebelumnya tidak pernah diteruskan ke klien SQL.
- Beban kerja Wasm cacat berpotensi menghentikan Strife secara paksa; kini
  tertangkap aman (*trap*).
- Contoh Kafka dan TiDB sebelumnya gagal menyala.
- Eksekusi sebelumnya menyiapkan `iptables` di semua node meskipun tanpa gangguan
  partisi.

## [0.1.0]

Engine inti: simulator deterministik beserta Raft dan tiga varian bug, checker
linearizability WGL dengan diagnosis dan saksi minimal, nemesis ber-seed
(`partition`, `latency`, `loss`, `kill`, `pause`, `freeze`, `io-stall`,
`clock-skew`), uji cluster langsung ke etcd dan Redis/Valkey, `strife shrink`,
keluaran JUnit dan ringkasan job bersama GitHub Action, `strife-agent`, probe
server, beban kerja Wasm, kampanye beserta pelacak regresi, atestasi laporan
`in-toto`, serta dossier kepatuhan.

[Unreleased]: https://github.com/Kemalhafizh/strife/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/Kemalhafizh/strife/compare/0d20bac...v1.0.0
[0.8.0]: https://github.com/Kemalhafizh/strife/compare/0.7.0...0d20bac
[0.7.0]: https://github.com/Kemalhafizh/strife/compare/0.6.0...v0.7.0
[0.6.0]: https://github.com/Kemalhafizh/strife/compare/0.5.0...v0.6.0
[0.5.0]: https://github.com/Kemalhafizh/strife/compare/0.4.0...v0.5.0
[0.4.0]: https://github.com/Kemalhafizh/strife/compare/0.3.0...v0.4.0
[0.3.0]: https://github.com/Kemalhafizh/strife/compare/640c9bc...v0.3.0
[0.2.0]: https://github.com/Kemalhafizh/strife/compare/22a3dff...640c9bc
[0.1.0]: https://github.com/Kemalhafizh/strife/tree/22a3dff

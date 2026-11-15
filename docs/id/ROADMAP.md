# Peta Jalan Strife

> **Bahasa:** [English](../ROADMAP.md) · Bahasa Indonesia

Peta jalan ini menerjemahkan cetak biru arsitektur (`STRF-BLPRNT-01`) menjadi rencana bertahap. Ada dua hal yang menentukan urutannya:

- apa yang sudah dikerjakan oleh kode saat ini, dan
- fitur mana yang memberi nilai paling tinggi dengan upaya paling ringkas.

Ketika cetak biru berbeda dari kenyataan rekayasa, dokumen ini menjelaskannya secara terbuka beserta pertimbangan yang diambil. Produk bukti audit dan kepatuhan tidak boleh membuat klaim yang tidak dapat dibuktikan secara empiris.

## Prinsip Dasar

1. **Setiap klaim harus terukur.** Sebuah fitur baru dinyatakan selesai bila sudah disertai pengujian atau tolok ukur yang membuktikannya, dan README mengutip angka hasil pengukuran nyata tersebut.
2. **Determinisme bersifat mutlak.** Tidak ada perubahan yang boleh merusak pemutaran ulang identik bit demi bit di dalam simulator. Pengujian golden PRNG dan flag `--determinism` di CI menegakkan aturan ini.
3. **Nol dependensi di inti sistem.** Satu biner statis mandiri. Komponen opsional (agen, probe) boleh memanfaatkan kapabilitas sistem lokal masing-masing tanpa pernah membocorkan dependensi pihak ketiga ke pustaka inti.
4. **Verdict yang jujur.** `unknown` adalah hasil yang sah. Mengembalikan `linearizable` secara keliru pada riwayat yang cacat adalah jenis bug paling fatal dalam proyek ini.

## Posisi Awal (v0.1)

| Butir Cetak Biru | Status |
| --- | --- |
| Pembangkit seed deterministik (PRNG, jadwal) | ✅ Selesai, ditambah simulator *discrete-event* penuh ber-hash jejak |
| Checker linearizability WGL | ✅ Selesai: memoisasi, paralelisme per kunci, reduksi simetri, uji silang brute-force |
| Model spesifikasi sekuensial | ✅ Selesai (`kv`, `set`, `queue`, `counter`, `idgen`, `txn`) |
| Checker anomali transaksional (bergaya Elle) | ✅ Selesai: Direct Serialization Graph, siklus (G0, G1a, G1b, G1c, G2, G-realtime) |
| Pelapor pelanggaran HTML | ✅ Selesai (HTML, Markdown, JSON, sidik jari) |
| Pelapor JUnit | ✅ Selesai pada Fase 1.2: `--junit`, `--summary`, dan GitHub Action |
| CLI Mandiri | ✅ Selesai (`sim`, `check`, `plan`, `run`, `systems`) |
| Target etcd | ✅ Klien dan contoh konfigurasi, tervalidasi pada Docker sungguhan di CI |
| Target Redis | ✅ Klien Redis/Valkey dan contoh konfigurasi, tervalidasi pada Docker sungguhan di CI |
| Target PostgreSQL | ✅ Klien protokol v3.0 pustaka standar Go murni & contoh Docker Compose (Fase 2.3) |
| Target Kafka | ✅ Klien protokol biner pustaka standar Go murni & contoh Docker Compose (Fase 2.3) |
| Demo Raft | ✅ Selesai, dilengkapi tiga sakelar bug yang tertangkap oleh Strife |
| `strife-shrink` (*delta debugging*) | ✅ Selesai pada Fase 1.1: `strife shrink`, terukur di bawah |
| `strife-agent` & netlink | ✅ Selesai pada Fase 2.1: `cmd/strife-agent`, IPC soket, netlink dan syscall langsung, terukur di bawah |
| Probe sisi server & beban kerja Wasm | ✅ Selesai pada Fase 2.4 (`pkg/probe`) & Fase 2.5 (`pkg/workload`, VM Wasm 1.0 Go murni) |
| Daemon inti Rust | ❎ Ditunda: Go sudah memenuhi sasaran satu biner tanpa dependensi eksternal |

## Fase 1: Penyelesaian MVP (Sumber Terbuka, Apache 2.0)

Sasaran: menutup celah pada Fase 1 cetak biru dan merilis pembeda terkuat sejak dini.

### 1.1 `strife shrink`: minimisasi otomatis ✅ selesai

Begitu sebuah pelanggaran ditemukan, Strife memperkecil skenario hingga bentuk minimal yang masih memicunya.

- **Simulator lebih dahulu.** Karena deterministik, *delta debugging* bersifat sah (*sound*): buang sebagian skenario, putar ulang dengan seed yang sama, lalu pertahankan pengurangannya bila pelanggaran tetap muncul.
- **Tahap-tahap:**
  - durasi,
  - `ddmin` atas himpunan gangguan,
  - target node gangguan,
  - jendela waktu gangguan (kedua ujung),
  - pembulatan waktu gangguan ke kisi 100ms/10ms/1ms (batas waktu yang kritis tetap dipertahankan eksak dan dilaporkan),
  - magnitudo gangguan,
  - jumlah klien,
  - tahap durasi kedua,
  - pemeriksaan akhir yang menjamin **1-minimalitas**: menghapus satu gangguan mana pun yang tersisa akan menghilangkan pelanggarannya.
- **Deterministik.** Kandidat dijalankan secara paralel tetapi dipilih dalam urutan tetap, sehingga masukan yang sama selalu menghasilkan skenario minimal yang sama terlepas dari jumlah *worker*. Skenario akhir dijalankan ulang dan diputar kembali dengan hash jejak yang identik.
- **Keluaran:** direktori `…-min/` berisi `schedule.json` minimal, laporan dengan bagian sebelum/sesudah "Shrinking", `shrink.json` untuk setiap tahap, serta perintah reproduksi.
- **Catatan batas:** menghapus sebuah gangguan mengubah aliran bilangan acak setelahnya. Kriteria pemangkasan adalah "tetap melanggar", bukan "anomali yang persis sama". Laporan mencatat hal ini secara eksplisit.

**Hasil terukur** (5 node, beban kerja bawaan, seed pertama yang melanggar dari tiap sistem):

| Sistem | Gangguan | ≤ 3 gangguan | Distribusi jumlah gangguan minimal | Median eksekusi / waktu |
| --- | --- | --- | --- | --- |
| `raft-stale-reads` | partition | **20/20 (100%)** | 1×15, 2×4, 3×1 | 80 / 4,6d |
| `raft-lease` | clock-skew, partition | **8/10 (80%)** | 2×5, 3×3, 4×2 | 146 / 7,4d |
| `raft-unsafe-fsync` | kill, io-stall | **7/7 (100%)** | 2×5, 3×2 | 119 / 11,7d |
| `raft-stale-reads` | kedelapan jenis | **14/15 (93%)** | 1×9, 2×4, 3×1, 5×1 | 84 / 4,0d |

Kriteria selesai (≥ 80% seed melanggar tersusut menjadi ≤ 3 gangguan untuk tiap sistem ber-bug): **terpenuhi**. Di ketiga konfigurasi target angkanya adalah 35 dari 37 seed (95%). Dua seed lease yang membutuhkan 4 gangguan bersifat 1-minimal, sehingga keempat gangguan tersebut memang benar-benar dibutuhkan agar partisi, lompatan jam, dan partisi kedua sejajar waktunya.

Pengembangan lanjutan pengecilan:

- **Uji cluster sungguhan:** jalankan ulang jadwal yang diperkecil. Hasilnya bersifat probabilistik sehingga wajib dilabeli sebagai "kandidat", bukan pembuktian matematis.
- **Pengecilan riwayat:** ✅ Selesai pada Fase 1.1 / 1.4: `strife shrink --history FILE` mereduksi contoh tandingan ribuan operasi menjadi himpunan operasi bertentangan paling kecil (umumnya 2–4 operasi) dengan verifikasi 1-minimalitas yang sah (*sound*).

### 1.2 Keluaran JUnit dan GitHub Action ✅ selesai

- `--junit FILE` dan `--summary FILE` pada `sim`, `shrink`, `check`, dan `run`. Keduanya lahir dari satu model suite yang sama agar XML dan Markdown selalu konsisten.
- Kasus uji:
  - satu per kunci untuk eksekusi tunggal,
  - satu per seed untuk eksplorasi,
  - satu kasus `finds a violation` untuk perburuan bug dengan `--expect violation`.
- Kegagalan membawa contoh tandingan dan perintah reproduksi.
- Pemeriksaan yang tak konklusif dan eksekusi yang gagal berjalan dicatat sebagai `error` JUnit, bukan lulus.
- Eksekusi langsung menambahkan kasus uji `fault injection`. Kasus ini bernilai error bila perintah gangguan gagal, lalu `strife run` keluar dengan kode non-nol: verdict bersih tidak bermakna bila gangguan yang direncanakan tidak pernah terlaksana.
- Properti suite memuat jejak audit: seed, verdict, sidik jari, SHA-256 jadwal, riwayat dan jejak, serta perintah reproduksi.
- `action.yml` adalah GitHub Action komposit yang membangun Strife dari sumber terpatok atau mengunduh biner terverifikasi, menjalankan perintah, menulis ringkasan ke `$GITHUB_STEP_SUMMARY`, mengunggah artefak, memberi anotasi eksekusi, mengekspor output `exit-code` dan `outcome`, serta menggagalkan job kecuali `fail: false`.
- Logika runner berada di `scripts/github-action.sh` agar dapat diperiksa oleh `shellcheck` dan diuji di luar GitHub. Argumen dipecah langsung, tidak pernah dievaluasi shell.
- Diuji pada diri sendiri (*dogfooded*): CI repositori ini memakai Action-nya sendiri (`uses: ./`) dan memeriksa output serta berkas JUnit-nya, sekaligus menjalankan `actionlint` dan `shellcheck`.
- Dokumentasi: [github-action.md](github-action.md), memuat resep untuk GitLab, Jenkins, CircleCI, dan Azure.

### 1.3 Klien Redis / Valkey dan validasi nyata ✅ selesai

- **Klien RESP2** ([`pkg/client/redis.go`](../../pkg/client/redis.go), pustaka standar murni):
  - operasi: `GET`, `SET`, serta *compare-and-set* sebagai skrip Lua `EVAL` atomik;
  - klasifikasi galat: balasan galat (mis. `READONLY`) atau koneksi ditolak menjadi `fail` pasti; timeout atau koneksi terputus setelah mengirim penulisan menjadi `info`; setiap galat I/O langsung membuang koneksi agar balasan terlambat tidak pernah dikaitkan ke perintah berikutnya;
  - pengujian: server RESP tiruan mematok perilaku protokol dan setiap jalur klasifikasi, ditambah uji terhadap `redis-server` sungguhan bila tersedia.
- **Namespace kunci per eksekusi:** setiap eksekusi langsung menulis di bawah prefiksnya sendiri (tercatat di laporan) agar eksekusi kedua terhadap cluster yang sama tidak melihat nilai sisa dari eksekusi pertama.
- **Validasi nyata di Docker**, berjalan permanen di CI (`live.yml`) melalui GitHub Action:

  | Cluster | Gangguan | Hasil |
  | --- | --- | --- |
  | etcd 3.5, 3 node | partition, kill, pause (+ latency, loss bila kernel punya netem) | ✅ linearizable, ~16 rb ops per menit |
  | Valkey 8, primary + 2 replika asinkron, bacaan menyebar | partition, pause | ❌ pembacaan basi sesuai ekspektasi (`--expect violation`) |
  | Valkey 8, primary tunggal, AOF | `kill -9` | ✅ linearizable: penulisan terkonfirmasi selamat dari penghentian paksa proses |

- **Flag baru:** `run --expect linearizable|violation` dan `run --faults` untuk menyesuaikan himpunan gangguan di kernel tanpa `sch_netem`.

### 1.4 Penguatan checker & pengecilan riwayat ✅ selesai

- **Pengecilan riwayat (`strife shrink --history FILE`):** mereduksi riwayat melanggar ribuan operasi menjadi himpunan konflik minimal (umumnya 2–4 operasi) disertai filter `isSoundHistory` (mencegah penghapusan penulisan yang memasok nilai bagi pembacaan tersisa) dan jaminan 1-minimalitas.
- **Anggaran batas waktu per kunci (`--check-timeout`):** membatasi waktu jam dinding tiap partisi di samping `--max-steps` agar riwayat patologis tidak memicu pencarian tanpa batas.
- **Penjelasan contoh tandingan terarah:** mengidentifikasi otomatis penulisan/mutasi selesai terbaru yang langsung membantah pembacaan bermasalah, lengkap dengan stempel waktu dan selisih waktunya.
- **Suite tolok ukur (`go test -bench`):** tervalidasi pada 1.000 hingga 100.000 operasi per kunci register dengan throughput berkelanjutan 95–508 rb ops/dtk per inti dan diagnosis pelanggaran ~1,5 ms pada 10.000 operasi ($O(N \log N)$).

## Fase 2: Ekstensibilitas (Inti Sumber Terbuka)

### 2.1 `strife-agent` dengan injeksi soket / netlink ✅ selesai

Cetak biru mengidentifikasi dengan tepat bahwa injeksi berbasis shell (`docker exec` dan `ssh`) membawa beban tambahan (*overhead*) dan jitter tinggi. Tonggak arsitektur ini mengganti pemanggilan shell dengan daemon per node dan manipulasi kernel langsung:

- **Daemon per node (`cmd/strife-agent`, `pkg/agent`):** melayani `/health`, `/apply`, `/exec`, `/teardown`, `/probes` lewat HTTP / Unix domain socket dengan koneksi persisten *keep-alive* dan semantik idempoten.
- **Linux Netlink (`pkg/agent/netlink_linux.go`):** konfigurasi qdisc `netem` dan aturan blackhole FIB langsung melalui `AF_NETLINK` / `NETLINK_ROUTE`.
- **Sinyal Langsung & cgroup v2 (`pkg/agent/process.go`, `pkg/agent/cgroup.go`):** mengganti `pkill` dengan pemindaian `/proc` langsung dan `syscall.Kill`, serta penulisan langsung ke `cgroup.freeze` dan `io.max`.
- **Transport Cepat (`pkg/transport/agent.go`):** mencatat waktu perjalanan bolak-balik dan waktu kerja di dalam agen pada setiap transisi.

**Latensi injeksi terukur** (`strife bench inject`, 50 transisi per baris, runner GitHub, cluster etcd 3-node; metode dan tabel lengkap di [`BENCHMARKS.md`](BENCHMARKS.md#1-latensi-injeksi-gangguan)):

| Gangguan | `docker exec` P50 | `strife-agent` P50 | Di dalam agen (P50) |
| --- | --- | --- | --- |
| pause | 43,7 ms | 1,36 ms | 372 µs |
| kill | 44,4 ms | 1,58 ms | 495 µs |
| partition | 57,9 ms | 4,02 ms | 2,61 ms (masih menjalankan `iptables`) |
| latency | 42,8 ms | 48,1 ms (balasan ikut tertunda gangguan) | 159 µs |

### 2.2 Model baru

- **Primitif sekuensial** ✅ Selesai: `queue` (antrean FIFO), `counter` (penghitung integer), `idgen` (pembangkit ID unik).
- **Deteksi anomali transaksional** (deteksi siklus bergaya Elle) ✅ Selesai: Direct Serialization Graph (`wr`, `ww`, `rw`, `so`, `ro`), komponen terhubung kuat Tarjan & siklus terpendek, anomali Adya (1999) `G0`, `G1a`, `G1b`, `G1c`, `G2`, `G-realtime` pada enam level isolasi melalui `--model txn` / `--model append` dan `--isolation <level>`.

### 2.3 Target database baru ✅ selesai

- **PostgreSQL** (`pkg/client/postgres.go`, protokol wire v3.0 Go stdlib murni): SCRAM-SHA-256, MD5, TLS, transaksi multi-pernyataan, dan pemetaan SQLSTATE yang jujur (`40001`/`40P01`/`23505` → `fail`; putus/timeout setelah `COMMIT` → `info`), disertai contoh cluster primer + standby di `examples/postgres/`.
- **Kafka** (`pkg/client/kafka.go`, protokol biner KRaft Go stdlib murni): ApiVersions, Metadata v4, Produce v3 (`acks=all`), Fetch v4, RecordBatch v2 CRC-32C, disertai contoh KRaft di `examples/kafka/`.
- **MySQL / TiDB** (`pkg/client/mysql.go`, protokol Client/Server v10 Go stdlib murni): `caching_sha2_password` jalur cepat maupun pertukaran RSA/TLS penuh, `mysql_native_password`, TLS, `init_sql`, serta kode abort server (`1213`, `1205`, `9007`, `9008`), disertai contoh di `examples/mysql/` dan `examples/tidb/`.

### 2.4 Probe Diagnostik Sisi Server & Korelator Akar Masalah (`pkg/probe`) ✅ selesai

Cetak biru awalnya mengusulkan perekaman riwayat lewat eBPF. Itu **bukan** pendekatan yang tepat bagi riwayat operasi itu sendiri: checker membutuhkan semantik operasi (kunci, nilai, hasil), yang dicatat oleh klien secara langsung hampir tanpa biaya tambahan.

Sebagai gantinya, telemetri sisi server diimplementasikan untuk **observasi akar masalah dan korelasi** mendampingi riwayat klien (`pkg/probe`):

- **Pengumpul Telemetri Sisi Server (`pkg/probe/collector.go`):** memantau Linux PSI (`/proc/pressure/io`, `/proc/pressure/cpu`), kejadian `oom_kill` di `memory.events` cgroup v2, latensi kenari `fsync` aktif & galat `EIO`/`ENOSPC`, lompatan jam dinding (`clock_jump`), serta sinyal proses (`GET /probes` di `strife-agent`).
- **Korelator Akar Masalah (`pkg/probe/correlate.go`):** mencocokkan jendela contoh tandingan dan operasi `info` terhadap kejadian `probes.jsonl` serta gangguan nemesis aktif untuk memberi atribusi akar masalah berperingkat di `report.md` dan `report.json`.

### 2.5 Plugin Beban Kerja Kustom & Wasm Runner (`pkg/workload` & `strife workload`) ✅ selesai

- **Mesin Stack WebAssembly 1.0 (MVP) tanpa dependensi (`pkg/workload/wasm.go`):** parser, validator, dan interpreter bytecode Wasm murni pustaka standar Go dengan anggaran instruksi (`DefaultMaxFuel = 10.000`), batas kedalaman panggilan (`32`), memori linier maksimal 1 MiB, dan ABI deterministik `next_op`.
- **Kompiler Spesifikasi Deklaratif & Preset (`pkg/workload/workload.go`):** mengompilasi JSON dan 6 preset bawaan (`kv-balanced`, `read-heavy`, `write-heavy`, `cas-hotspot`, `sequential-scan`, `si-write-skew`) menjadi berkas `.wasm` mandiri.

## Fase 3: Runner Cloud & Visualizer (Open-Core SaaS)

### 3.1 Penganalisis Timeline Interaktif (`report.html`) ✅ selesai

Studio HTML5/Canvas mandiri luring tanpa panggilan CDN eksternal: *pan/zoom* 60fps High-DPI, tombol fokus anomali otomatis, filter multidimensi (kunci, ID proses, status, lapisan gangguan), scrubber mini-map, inspeksi operasi JSON, serta fallback SVG `<noscript>` untuk pemirsa headless.

### 3.2 Runner Kampanye & Dasbor Regresi (`pkg/campaign` & `strife campaign`) ✅ selesai

Mesin eksekusi paralel multi-seed dengan sidik jari kampanye SHA-256, perbandingan regresi antar-commit (`regressed`, `fixed`, `persistent_failure`, `new_failure`, `trace_drift`), pemutaran ulang korpus gagal prioritas (`--replay-failing`), dasbor heatmap interaktif `campaign.html`, serta integrasi `--expect no-regression`, `--junit`, dan `--summary`.

## Fase 4: Kepatuhan & Atestasi (Siap Audit Enterprise)

### 4.1 Penandatanganan Laporan Kriptografis & Atestasi in-toto (`pkg/attest`) ✅ selesai

Mengimplementasikan amplop Pernyataan `https://in-toto.io/Statement/v0.1` dan predikat `https://strife.dev/attestation/v1` bertanda tangan Ed25519 murni pustaka standar Go melalui perintah `strife keygen`, `strife sign`, dan `strife verify`.

### 4.2 Pemetaan Audit Regulasi & Dossier Kepatuhan (`pkg/compliance` & `strife compliance`) ✅ selesai

Memetakan properti ketahanan dan konsistensi empiris ke enam kerangka audit dan regulasi beserta kutipan ketentuannya: **EU DORA**, **AICPA SOC 2**, **ISO/IEC 27001:2022**, **OJK (POJK No. 11/POJK.03/2022 & SEOJK No. 29/SEOJK.03/2022)**, **PCI DSS v4.0 / v4.0.1**, dan **Bank Indonesia Sistem Pembayaran (PBI No. 23/6/PBI/2021 & PBI No. 23/7/PBI/2021)**. Setiap kontrol bernilai `PASS`, `PARTIAL`, atau `FAIL` dilengkapi bagian **Metodologi & Batasan Cakupan** dalam keluaran `dossier.json`, `dossier.md`, dan `dossier.html`.

## Fase 5: Pembuktian, Jangkauan, Distribusi ✅ selesai (PR #8)

Audit terhadap `main` pada `22a3dff` (2026-10-17) memastikan setiap klaim fitur didukung bukti empiris dalam CI: tolok ukur injeksi agen melalui `strife bench inject`, job cluster nyata untuk etcd/PostgreSQL/MySQL/TiDB/Kafka/Valkey/Kubernetes/cgroup, 8 target fuzzing parser di CI, uji silang brute-force (`0 / 150.030` riwayat valid salah ditandai), skema JSON v1.0 beku di `schemas/`, serta pilar rekayasa utama versi `v0.2.0` hingga `v1.0.0`:

- **Pemeriksa buku besar keuangan berpasangan & konservasi uang** (`--model ledger`, `raft-ledger`, `raft-ledger-overdraft`, `2pc-ledger`, `2pc-ledger-torn-commit`): membuktikan `LEDGER-CONSERVATION`, `LEDGER-OVERDRAFT`, `LEDGER-DOUBLE-SPEND`, dan `LEDGER-UNBALANCED` pada simulasi maupun cluster nyata.
- **Pemangkasan interval nilai sub-kuadratik pada solver WGL (`kvPruner`) & reduksi transitif DSG:** memangkas 10.000 operasi di 50 proses konkuren dari `> 50.000.000` langkah (`unknown`) menjadi **22.913 langkah** (~534 ms).
- **Partisi kernel native lewat policy routing `AF_NETLINK`** (`RTM_NEWRULE` / `FR_ACT_BLACKHOLE`) dengan fallback otomatis ke `iptables`, serta penemuan pod/netns Kubernetes otomatis di `strife-agent`.
- **Daemon registri run & worker jarak jauh berbasis pull** (`strife serve`, `strife worker`), importir trace OpenTelemetry Span JSON (`pkg/history/otel.go`), gerbang PR komparatif `strife ci-report`, proxy gangguan L4 TCP tanpa hak root (`transport.type: "proxy"`), serta model verifikasi kliring & outbox (`--model settlement`) beserta gerbang SLA `--max-mttr` dan `--min-availability`.

## Fase 6: Lapisan Komersial (Berikutnya)

- **Mitra desain (*design partner*):** 2–3 bank atau fintech di Indonesia (`bi-ojk`, `bi-sp`, dan `pci-dss`) yang menjalankan Strife di lingkungan staging bersama `strife serve` + `strife worker`, dengan ukuran keberhasilan penggunaan dossier dalam proses audit nyata.
- **Skema penawaran:** CLI dan Action gratis di bawah Apache 2.0; paket komersial mencakup runner terkelola di cloud, penyusunan dossier tertanda tangan, serta SLA dukungan.

## Pedoman Penulisan

Klaim yang dipakai di dokumentasi dan publikasi wajib mengikuti batasan berikut:

- **Pemutaran ulang:** nyatakan sebagai *pemutaran ulang identik bit demi bit di simulator; jadwal gangguan dan aliran beban kerja identik pada cluster sungguhan*.
- **Kecepatan checker:** gunakan angka nyata hasil suite tolok ukur lengkap dengan ukuran riwayat dan tingkat konkurensinya.
- **Perekaman vs eBPF/probe:** bedakan jelas perekaman riwayat di sisi klien dari telemetri sisi server untuk korelasi akar masalah.
- **Pengecilan skenario:** nyatakan angka terukur (*menyusut menjadi $\le 3$ gangguan pada 95% seed yang melanggar dalam korpus tolok ukur kami, dengan 1-minimalitas pada semuanya*).

## Monetisasi

Konsisten dengan lisensi saat ini:

- **Gratis, Apache 2.0:** engine inti (simulator, checker, nemesis, CLI, agent, registri `serve`, worker) tetap 100% terbuka untuk membangun kepercayaan teknis dan komunitas.
- **Komersial:** layanan penganalisis visual terkelola, runner cloud berskala besar, serta dukungan audit dan penyusunan dossier bertanda tangan untuk perbankan, fintech, dan vendor database.

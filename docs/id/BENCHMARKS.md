# Tolok Ukur dan Validasi

> **Bahasa:** [English](../BENCHMARKS.md) · Bahasa Indonesia

Setiap angka di dokumentasi Strife berasal dari halaman ini, dan setiap angka di halaman ini disertai perintah yang menghasilkannya, mesin tempat ia dijalankan, serta tanggalnya. Angka tanpa cara reproduksi tidak masuk ke dalam dokumentasi utama.

Diukur pada 2026-10-17 pada commit `e5527d8` (CI) dan pada pohon kerja cabang yang sama (lokal). Runner CI bersama memiliki variasi beban (*noisy*): perlakukan satu kali eksekusi sebagai sampel, bukan jaminan mutlak. Bila dua kali eksekusi berbeda, keduanya dicantumkan.

## 1. Latensi Injeksi Gangguan

**Pertanyaan:** ketika Strife menjadwalkan sebuah gangguan pada saat *t*, seberapa terlambat gangguan tersebut benar-benar berlaku?

**Metode:** `strife bench inject` menerapkan satu jenis gangguan sekaligus ke satu node (bergantian melintasi cluster), pada periode tetap, terhadap cluster etcd 3-node di Docker, lalu mencatat untuk setiap transisi jeda waktu dari saat rencana hingga setiap node terkait mengonfirmasi perubahan tersebut. 50 siklus per baris (injeksi dan pemulihan adalah baris terpisah), satu transisi setiap 300 ms. Untuk `strife-agent`, "sisi agen" (*agent-side*) adalah waktu di dalam agen sejak menerima permintaan hingga panggilan kernel selesai (node paling lambat).

```sh
# .github/workflows/live.yml, job "agent"
bin/strife bench inject --config examples/etcd/strife-agent.json \
  --transports docker,agent --faults partition,pause,kill,latency,loss \
  --cycles 50 --period 300ms --markdown bench-inject.md --json bench-inject.json
```

**Host:** `ubuntu-24.04` milik GitHub, 4 vCPU AMD EPYC 9V74, Linux 6.17.0-1022-azure, Go 1.23.12. Eksekusi [39039844498](https://github.com/Kemalhafizh/strife/actions/runs/39039844498).

| Transport | Gangguan | Sisi Transisi | P50 | P95 | Maks | Sisi agen P50 / P95 | Galat |
|---|---|---|---|---|---|---|---|
| docker exec | partition | inject | 57,9 ms | 61,3 ms | 63,2 ms | – | 0 / 50 |
| docker exec | partition | heal | 59,4 ms | 64,0 ms | 77,9 ms | – | 0 / 50 |
| docker exec | pause | inject | 43,7 ms | 45,1 ms | 46,3 ms | – | 0 / 50 |
| docker exec | kill | inject | 44,4 ms | 46,1 ms | 46,8 ms | – | 0 / 50 |
| docker exec | latency | inject | 42,8 ms | 44,1 ms | 45,3 ms | – | 0 / 50 |
| docker exec | loss | inject | 42,8 ms | 45,6 ms | 47,2 ms | – | 0 / 50 |
| strife-agent | partition | inject | 4,02 ms | 4,29 ms | 4,41 ms | 2,61 ms / 2,81 ms | 0 / 50 |
| strife-agent | partition | heal | 7,44 ms | 9,58 ms | 10,5 ms | 5,99 ms / 8,53 ms | 0 / 50 |
| strife-agent | pause | inject | 1,36 ms | 1,90 ms | 2,76 ms | 372 µs / 489 µs | 0 / 50 |
| strife-agent | pause | heal | 1,43 ms | 1,96 ms | 2,24 ms | 410 µs / 505 µs | 0 / 50 |
| strife-agent | kill | inject | 1,58 ms | 1,99 ms | 2,04 ms | 495 µs / 707 µs | 0 / 50 |
| strife-agent | kill | heal | 1,41 ms | 1,91 ms | 1,93 ms | 331 µs / 377 µs | 0 / 50 |
| strife-agent | latency | inject | 48,1 ms ¹ | 59,5 ms ¹ | 60,7 ms ¹ | 159 µs / 185 µs | 0 / 50 |
| strife-agent | latency | heal | 1,11 ms | 1,68 ms | 1,72 ms | 158 µs / 189 µs | 0 / 50 |
| strife-agent | loss | inject | 1,23 ms | 235 ms ¹ | 834 ms ¹ | 147 µs / 168 µs | 0 / 50 |
| strife-agent | loss | heal | 1,08 ms | 1,72 ms | 535 ms ¹ | 158 µs / 183 µs | 0 / 50 |

¹ Konfirmasi dari agen keluar melewati antarmuka jaringan yang baru saja diganggunya: dengan perlambatan netem 50 ms ± 10 ms balasannya sendiri ikut tertunda ~50 ms, dan dengan kehilangan paket 20% terkadang diperlukan retransmisi TCP. Gangguan sudah berlaku tepat saat panggilan kernel selesai (kolom sisi agen); hanya konfirmasinya yang tiba terlambat. Baris `heal` milik Docker (dihilangkan agar ringkas) berjarak dalam ±2 ms dari baris `inject`.

**Membaca hasil:**

- `docker exec` menghabiskan ~43–60 ms per transisi, hampir seluruhnya di CLI Docker dan perjalanan bolak-balik (*round trip*) `exec`, dengan sebaran sempit ($\sigma \approx 1\text{–}3\text{ ms}$).
- `strife-agent` mendaratkan `pause` dan `kill` dalam ~1,4–1,6 ms pada P50 dan di bawah 3 ms pada nilai maksimum: ~30× lebih cepat daripada `docker exec`. Di dalam agen sendiri, kerja kernel memakan 330–500 µs untuk sinyal proses dan ~150–190 µs untuk perubahan netem.
- Partisi lewat agen pada run `39039844498` memakan 4–7 ms (memakai biner fallback `iptables`); kini `strife-agent` menyuntikkan partisi melalui aturan blackhole FIB `AF_NETLINK` native (`RTM_NEWRULE` / `FR_ACT_BLACKHOLE`) ketika kernel mengizinkan *policy routing*, dengan fallback otomatis ke `iptables`.
- Pengukuran sebelumnya pada runner AMD EPYC 7763 ([39038949984](https://github.com/Kemalhafizh/strife/actions/runs/39038949984)) mencatat P50 docker 51–76 ms dan agen `pause`/`kill` 1,40–1,70 ms: urutannya tetap konsisten, sedangkan nilai absolutnya mengikuti perangkat keras runner.

**Gangguan cgroup** (`freeze`, `io-stall`) berjalan di Docker host, bukan di dalam kontainer, sehingga tidak melalui `docker exec`. Job `cgroup`, run yang sama, AMD EPYC 7763, 20 siklus setiap 500 ms:

| Gangguan | Sisi Transisi | P50 | P95 | Maks | Galat |
|---|---|---|---|---|---|
| freeze | inject | 9,56 ms | 10,1 ms | 10,3 ms | 0 / 20 |
| freeze | heal | 9,63 ms | 10,3 ms | 10,5 ms | 0 / 20 |
| io-stall | inject | 8,33 ms | 8,74 ms | 8,95 ms | 0 / 20 |
| io-stall | heal | 8,30 ms | 9,78 ms | 10,3 ms | 0 / 20 |

`freeze` hanya dinyatakan berhasil setelah `cgroup.events` melaporkan `frozen 1`, bukan saat penulisan ke `cgroup.freeze` kembali. Job yang sama memastikan bahwa `io.max` benar-benar menahan penulisan disk kontainer ([`scripts/live/check-io-stall.sh`](../../scripts/live/check-io-stall.sh)).

**Belum diukur:** transport `ssh` dan `kubernetes` (keduanya mengeksekusi shell per perintah, sehingga perkirakan orde waktu yang sama dengan `docker exec` ditambah latensi jaringan) serta `clock-skew`.

## 2. Throughput Checker

`go test -bench` di pohon kerja, satu inti CPU per pemeriksaan (`Parallelism: 1`).
Host: 4 vCPU Intel Xeon @ 2,10 GHz, Linux 6.18, Go 1.24.7.

```sh
go test ./pkg/checker -run '^$' -bench 'Linearizable|Indeterminate|Violation' -benchtime 3x
go test ./pkg/txn     -run '^$' -bench CheckAppend -benchtime 3x
```

**Linearizability (pencarian WGL dengan memoisasi), satu register, seluruh operasi mengenai kunci yang sama** (kasus terburuk bagi P-komposisionalitas):

| Riwayat | Proses konkuren | Penulisan tak pasti (`info`) | Throughput | Langkah / op |
|---|---|---|---|---|
| 1.000 ops | 5 | 0 % | 508 rb ops/dtk | 1,4 |
| 10.000 ops | 5 | 0 % | 408 rb ops/dtk | 1,2 |
| 100.000 ops | 5 | 0 % | 477 rb ops/dtk | 1,3 |
| 1.000 ops | 20 | 0 % | 98 rb ops/dtk | 41 |
| 10.000 ops | 20 | 0 % | 107 rb ops/dtk | 33 |
| 100.000 ops | 20 | 0 % | 95 rb ops/dtk | 30 |
| 1.000 ops | 50 | 0 % | 63 rb ops/dtk | 1,1 |
| 10.000 ops | 50 | 0 % | 18,7 rb ops/dtk | 2,3 |
| 10.000 ops | 10 | 1 % | 301 rb ops/dtk | 3,0 |
| 10.000 ops | 10 | 5 % | 344 rb ops/dtk | 3,0 |

Pemangkasan interval nilai ([`kvPruner`](../../pkg/checker/linearizable.go)) merambatkan jendela efektif transitif `[effCall, effReturn]` melintasi penulisan ber-pembuat tunggal (*sole-creator*) dan *compare-and-swap*, memangkas transisi nilai yang tak mungkin dijangkau via pencocokan celah mati serta mengurutkan kandidat terbuka berdasarkan tenggat pengamat paling awal yang belum terpenuhi. Pada 10.000 operasi di 50 proses konkuren yang berebut satu kunci tunggal, teknik ini menurunkan jumlah langkah pencarian dari `> 50.000.000` (`unknown`) menjadi **22.913 langkah** (**2,29 langkah/op**, ~534 ms). Beban kerja nyata menyebarkan operasi ke beberapa kunci yang diperiksa secara independen.

**Membuktikan pelanggaran:** pembacaan basi didiagnosis langsung tanpa menghabiskan pencarian penuh: 1,1 juta ops/dtk pada 1.000 ops, 527 ribu ops/dtk pada 100.000 ops (baik dengan maupun tanpa penulisan tak pasti). Pencarian murni sebagai pembanding menolak di kecepatan ~35 ribu ops/dtk.

**List-append (graf kebergantungan bergaya Elle), strict serializability, 10 kunci, transaksi hingga 8 mikro-operasi:** 41 ribu txn/dtk pada 1.000 transaksi, 57 ribu txn/dtk pada 10.000, dan 48 ribu txn/dtk pada 50.000.

## 3. Ketepatan Checker

Uji silang terhadap *brute force* pada riwayat acak kecil di mana setiap urutan serial dapat dienumerasi:

```sh
STRIFE_APPEND_ITERS=200000 go test ./pkg/txn -run 'AppendCompleteness' -v
go test ./pkg/txn -run 'AppendAgainstBruteForce|RegisterTxnAgainstBruteForce' -v
```

| Checker | Jumlah riwayat | Riwayat valid salah ditandai | Riwayat cacat tertangkap |
|---|---|---|---|
| list-append, strict serializable | 200.000 (150.030 valid) | **0** | 49.877 / 49.970 (**99,81 %**) |
| transaksi register, serializable & strict | 40.000 | **0** | (hanya uji *soundness*) |

*Soundness* (tidak pernah menandai melanggar sebuah riwayat yang sebenarnya punya urutan sah) diwajibkan dan ditegakkan oleh seluruh rangkaian pengujian. Kelengkapan (*completeness*) tidak mungkin 100% penuh bagi list-append: sebuah anomali dapat bersembunyi di balik transaksi `info` yang tak pasti, atau pembacaan rusak dapat tetap terjelaskan oleh urutan versi lain. Seluruh 93 riwayat yang lolos berasal dari kasus semacam itu.

Fuzzing berkelanjutan (`ci.yml`, job `fuzz`) menjalankan `FuzzCheckAppendSound`, `FuzzReadJSONL`, `FuzzReadEDN`, `FuzzParseWasm`, serta dekoder protokol jaringan (`FuzzPostgresMessages`, `FuzzMySQLPackets`, `FuzzKafkaRecords`, `FuzzRESP`). Fuzzing sebelumnya menemukan panic *stack-underflow* pada interpreter Wasm dan *integer overflow* pada dekoder paket MySQL, keduanya telah diperbaiki; masukan pemicunya disimpan sebagai korpus regresi di bawah `pkg/*/testdata/fuzz`. Setiap dekoder membatasi alokasi memorinya berdasarkan byte yang benar-benar tiba, bukan panjang yang diklaim pada wire.

## 4. Deteksi Bug di Simulator

Setiap baris menyapu cluster 5-node selama 10 detik waktu simulasi (`--seeds 1..200`, atau `--seeds 1..60` untuk `raft-ledger-overdraft`, `2pc-ledger-torn-commit`, dan `outbox-dual-write`). Sistem yang memiliki bug terpasang selalu tertangkap, sedangkan implementasi referensi yang benar tidak pernah salah ditandai.

```sh
strife sim --system raft-stale-reads       --faults partition           --seeds 1..200
strife sim --system raft-lease             --faults clock-skew,partition --seeds 1..200
strife sim --system raft-unsafe-fsync      --faults kill,io-stall        --seeds 1..200
strife sim --system raft-ledger-overdraft  --faults partition,latency    --seeds 1..60
strife sim --system 2pc-ledger-torn-commit --faults partition,kill       --seeds 1..60
strife sim --system outbox-dual-write      --faults partition,kill       --seeds 1..60
strife sim --system outbox-settlement      --faults partition,latency,loss,kill,pause,freeze,io-stall,clock-skew --seeds 1..200 --determinism --expect linearizable
strife sim --system 2pc-ledger  --faults partition,latency,loss,kill,pause,freeze,io-stall,clock-skew --seeds 1..200
strife sim --system raft-ledger --faults partition,latency,loss,kill,pause,freeze,io-stall,clock-skew --seeds 1..200
strife sim --system raft        --faults partition,latency,loss,kill,pause,freeze,io-stall,clock-skew --seeds 1..200
```

| Sistem | Bug | Gangguan | Seed melanggar |
|---|---|---|---|
| `raft` | tidak ada | kedelapan jenis | **0 / 200** |
| `raft-ledger` | tidak ada (buku besar keuangan berpasangan di atas Raft) | kedelapan jenis | **0 / 200** |
| `2pc-ledger` | tidak ada (buku besar keuangan *two-phase prepare/commit*) | kedelapan jenis | **0 / 200** |
| `outbox-settlement` | tidak ada (alur kliring transaksional outbox dengan deduplikasi idempoten) | kedelapan jenis | **0 / 200** |
| `raft-stale-reads` | leader terdepak tetap melayani pembacaan lokal | partition | 75 / 200 (37,5 %) |
| `raft-lease` | masa sewa (*lease*) diukur dengan jam dinding | clock-skew, partition | 10 / 200 (5 %); 0 / 200 tanpa clock-skew |
| `raft-unsafe-fsync` | mengakui suara/entri sebelum `fsync` | kill, io-stall | 7 / 200 (3,5 %); 0 / 200 di bawah partition saja |
| `raft-ledger-overdraft` | cek saldo pra-replikasi memungkinkan overdraft konkuren (`LEDGER-OVERDRAFT`) | partition, latency | **60 / 60 (100 %)** |
| `2pc-ledger-torn-commit` | 2PC / Saga non-atomik meng-commit kaki debit sebelum kredit (`LEDGER-CONSERVATION`) | partition, kill | **60 / 60 (100 %)** |
| `outbox-dual-write` | layanan *dual-write* asinkron menghilangkan kejadian kliring saat crash/partisi (`SETTLEMENT-ATOMICITY`) | partition, kill | **60 / 60 (100 %)** |

Delapan penyapuan pertama memakan waktu 17–34 detik masing-masing di host 4-vCPU di atas. Penyapuan `outbox-settlement` (termasuk pemutaran ulang `--determinism`, 2m13d) dan `outbox-dual-write` (2m27d) diukur pada 2026-11-13 pada commit `8300f0a` di host 2 vCPU Intel Xeon Platinum 8481C (Linux 6.12.105+ x86_64, Go 1.27.3).

## 5. Cluster Sungguhan

Setiap push menjalankan [`.github/workflows/live.yml`](../../.github/workflows/live.yml). Setiap baris adalah asersi tegas: eksekusi yang tidak menghasilkan verdict yang diharapkan otomatis menggagalkan job CI.

| Job | Sistem | Gangguan | Diperiksa sebagai | Ekspektasi dan hasil teramati |
|---|---|---|---|---|
| etcd | etcd 3.5, 3 node | partition, kill, pause, latency ², loss ² | linearizable (kv) | lulus |
| etcd | sama, transaksi | sama | strict serializable (list-append) | lulus |
| agent | etcd via strife-agent | partition, kill, pause | linearizable | lulus |
| cgroup | etcd | freeze, io-stall | linearizable | lulus |
| kubernetes | etcd StatefulSet di kind | partition, kill, pause, latency ², loss ² | linearizable | lulus |
| valkey | Valkey 8, primary + 2 replika asinkron | partition, pause | linearizable | **pelanggaran** (pembacaan basi replika) |
| valkey | Valkey 8, primary tunggal, `appendfsync always` | kill -9 | linearizable | lulus |
| postgres | PostgreSQL 17 primary, SERIALIZABLE | kill -9, pause | strict serializable | lulus |
| postgres | PostgreSQL 17 primary, buku besar berpasangan (`SERIALIZABLE`) | kill -9, pause | strict serializable (`ledger`) | lulus |
| postgres | PostgreSQL 17, REPEATABLE READ | kill -9, pause | snapshot isolation | lulus |
| postgres | riwayat yang sama | – | serializable | **pelanggaran** (G2, *write skew*) |
| postgres | standby asinkron | partition | strict serializable | **pelanggaran** (G-realtime); `shrink --live` mereduksinya menjadi nol gangguan |
| mysql | MySQL 8.4, SERIALIZABLE | freeze | strict serializable | lulus |
| mysql | MySQL 8.4, REPEATABLE READ | none | snapshot isolation | **pelanggaran** (internal, G-single) |
| tidb | TiDB 8.5, optimistik | freeze | snapshot isolation | lulus |
| tidb | TiDB 8.5, pesimistik | none | snapshot isolation | **pelanggaran** (internal, G-single) |
| kafka | Kafka 3.9.1 dan 4.1.0 KRaft | partition, kill -9, pause | strict serializable (list-append) | tidak ada rekaman hilang atau terbalik; pembacaan basi (G-realtime) ditoleransi |

² Ketika kernel runner memiliki `sch_netem` (terpasang di seluruh eksekusi sejauh ini).

Eksekusi [39039844498](https://github.com/Kemalhafizh/strife/actions/runs/39039844498) lulus pada seluruh sepuluh job.

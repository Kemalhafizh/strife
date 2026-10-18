---
title: Di balik engine
description: Simulator deterministik, checker linearizability dan isolasi transaksi, klien database tanpa dependensi, injeksi gangguan tingkat kernel, dan artefak yang bisa diaudit.
eyebrow: Untuk engineer
lead: Strife ditulis dalam Go dengan pustaka standar saja. Seed 64-bit menentukan seluruh eksekusi di simulator, sedangkan pada cluster sungguhan seed menentukan jadwal gangguan dan aliran operasi setiap klien.
translationKey: engine
---

## Dua cara menguji

**Simulator deterministik** (`strife sim`). Cluster Raft lengkap berjalan di dalam satu proses bergaya FoundationDB. Jaringan, timer, flush disk, dan pergeseran jam semuanya dikendalikan oleh seed, sehingga satu eksekusi bisa diputar ulang bit per bit dan dibuktikan dengan digest SHA-256. Sepuluh detik simulasi selesai dalam hitungan ratusan milidetik, jadi ribuan jadwal gangguan bisa disapu dari satu laptop. Coba langsung di [playground](/coba).

**Cluster sungguhan** (`strife run`). CLI menjalankan klien terhadap etcd, PostgreSQL, MySQL/TiDB, Valkey, atau Kafka, lalu menyuntikkan gangguan lewat Docker, SSH, Kubernetes, atau `strife-agent`. Riwayatnya diperiksa dengan checker yang sama. Panduannya ada di [Live Clusters](/docs/live-clusters).

## Gangguan yang disuntikkan

| Gangguan | Mekanisme |
| :--- | :--- |
| `partition` | iptables `DROP` antara dua sisi cluster |
| `latency`, `loss` | `tc netem` (lewat agen: paket netlink langsung) |
| `kill` | `SIGKILL`, lalu proses dinyalakan lagi saat gangguan berakhir |
| `pause` | `SIGSTOP` / `SIGCONT` |
| `freeze` | freezer cgroup v2 |
| `io-stall` | `io.max` cgroup v2 diturunkan hingga hampir nol, sehingga `fsync` tertahan |
| `clock-skew` | laju jam dan lompatan jam dinding |

Lewat `strife-agent`, median keterlambatan injeksi pause adalah 1,36 ms, dibandingkan 43,7 ms lewat `docker exec` ([metode dan mesin](/docs/benchmarks#1-fault-injection-latency)).

## Checker

| Model | Yang dibuktikan |
| :--- | :--- |
| `kv` | Linearizability register baca/tulis/CAS (pencarian Wing & Gong / Lowe dengan memoisasi, P-compositionality per kunci) |
| `set`, `queue`, `counter`, `idgen` | Linearizability set, antrean FIFO, penghitung, dan pembangkit ID unik |
| `txn` | Isolasi transaksi lewat Direct Serialization Graph: anomali Adya G0, G1a, G1b, G1c, G2, dan G-realtime pada enam level isolasi |
| `append` | List-append bergaya Elle: G0–G2, G-single, G-realtime, append yang hilang, urutan yang tidak cocok, duplikat, dan konsistensi internal |

Bila anggaran pencarian habis, verdict-nya **tidak pasti**, tidak pernah lulus. Kecepatan dan ketepatan checker diukur di [benchmark](/docs/benchmarks#2-checker-throughput), termasuk batasnya. Detail setiap model ada di [Consistency Models](/docs/models).

## Klien database tanpa dependensi

Semua protokol diimplementasikan langsung di repositori: etcd v3, Redis/Valkey (RESP2, AUTH, TLS), PostgreSQL (SCRAM-SHA-256, TLS), MySQL/TiDB (`caching_sha2_password`, TLS), dan Kafka (Produce v3, Fetch v4, RecordBatch v2). Kredensial ditulis sebagai rujukan `env:` atau `file:` dan tidak pernah masuk ke laporan.

## Memperkecil kegagalan

`strife shrink` memakai delta debugging untuk memperkecil pelanggaran menjadi skenario **1-minimal**, artinya membuang satu gangguan atau operasi mana pun akan menghilangkan pelanggarannya. Untuk cluster sungguhan, hasilnya disebut *kandidat* lengkap dengan tingkat reproduksinya. Detailnya ada di [Shrinking](/docs/shrinking).

## Artefak yang bisa diaudit

Setiap run menghasilkan `report.html` (timeline interaktif yang bekerja tanpa jaringan), `report.json`, `history.jsonl`, dan `schedule.json` yang bisa diputar ulang. Contoh nyata:

- [Laporan timeline](/demos/timeline.html): `raft-unsafe-fsync` seed 13, setelah diperkecil
- [Dasbor kampanye regresi](/demos/campaign.html): 40 seed
- [Dossier kepatuhan](/demos/dossier.html): DORA, SOC 2, ISO 27001, dan OJK

## Beban kerja WebAssembly

Beban kerja kustom bisa ditulis sebagai modul WebAssembly 1.0 yang dijalankan interpreter internal dengan batas fuel, stack, dan memori, sehingga tetap deterministik dan aman dari loop tak berujung. Lihat [Custom Workloads](/docs/workloads).

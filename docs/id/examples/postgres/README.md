# Contoh: PostgreSQL 17 di bawah Strife

> **Bahasa:** [English](../../../../examples/postgres/README.md) · Bahasa Indonesia

Satu node primary (`n1`) dan dua standby panas asinkron (`n2`, `n3`), dengan autentikasi SCRAM-SHA-256 dan TLS. Klien Strife mengimplementasikan protokol wire v3 secara langsung (tanpa `libpq`), menjalankan beban kerja list-append, buku besar berpasangan (*double-entry ledger*), atau kliring (*settlement*), lalu memeriksa riwayat eksekusi yang terekam.

```sh
export STRIFE_PG_PASSWORD=$(openssl rand -hex 16)
cd examples/postgres
docker compose up -d --build

strife run --config strife.json --expect no-anomaly          # primary, list-append SERIALIZABLE, kill -9 + pause
strife run --config strife-ledger.json --expect no-anomaly   # primary, audit buku besar berpasangan SERIALIZABLE
strife run --config strife-snapshot.json --expect no-anomaly # primary, diperiksa sebagai snapshot isolation
strife run --config strife-replicas.json --expect violation  # pembacaan disebar ke node-node standby

docker compose down -v
```

Kata sandi tidak pernah tertulis secara harfiah di berkas konfigurasi: `"password": "env:STRIFE_PG_PASSWORD"` diselesaikan saat eksekusi berjalan dan laporan hanya menampilkan rujukannya, bukan nilainya.

`strife-settlement.json` adalah templat beban kerja dua-kaki `settlement` tambahan yang dikonfigurasi menggunakan transport `proxy` tanpa hak istimewa terhadap cluster PostgreSQL mandiri (`172.30.0.11:5432`, pengguna/database `strife`); sesuaikan `nodes` dan `db.endpoints` bila dijalankan terhadap jaringan compose lokal, dan gunakan `--expect no-anomaly` di CI (karena checker transaksional untuk `append`, `ledger`, dan `settlement` melaporkan `unknown (sound incomplete)` alih-alih pembuktian linearizability menyeluruh bila riwayat melampaui pencarian tuntas berbatas).

## Yang Diuji oleh CI (job `postgres` di `.github/workflows/live.yml`)

| Konfigurasi | Isolasi yang diminta ke PostgreSQL | Diperiksa sebagai | Gangguan | Hasil diharapkan |
|---|---|---|---|---|
| `strife.json` | `SERIALIZABLE` | strict serializable (`append`) | `kill`, `pause` | tanpa anomali (`--expect no-anomaly`) |
| `strife-ledger.json` | `SERIALIZABLE` | strict serializable (`ledger`) | `kill`, `pause` | tanpa anomali (`--expect no-anomaly`) |
| `strife-snapshot.json` | bawaan (*default*) | snapshot isolation (`append`) | `kill`, `pause` | tanpa anomali (`--expect no-anomaly`) |
| riwayat yang sama | | serializable | | **pelanggaran**: *write skew* (G2) |
| `strife-replicas.json` | `REPEATABLE READ` per standby | strict serializable (`append`) | `partition` | **pelanggaran**: pembacaan basi (G-realtime) |

Baris terakhir bukan merupakan bug PostgreSQL: standby asinkron memutar ulang WAL tertinggal dari primary, sehingga klien yang membaca dari salah satu standby dapat melewatkan transaksi yang sudah di-commit. `strife shrink --live` memastikan kasus ini tidak memerlukan gangguan sama sekali: jadwal minimalnya kosong dan tetap bereproduksi pada setiap putaran verifikasi.

```sh
strife shrink --live --config strife-replicas.json --duration 15s --attempts 2 --verify 3
```

## Opsi Penting

- `db.tls`: `require` (enkripsi tanpa verifikasi sertifikat), `verify-full` dengan `db.tls_ca` untuk bundel CA, atau kosongkan untuk teks polos.
- `db.txn_isolation` menetapkan level isolasi di sisi PostgreSQL (`BEGIN ISOLATION LEVEL ...`); `workload.isolation` adalah kontrak yang diperiksa checker. Memisahkan keduanya adalah cara contoh `snapshot` mendemonstrasikan celah di antara keduanya.
- Galat yang ambigu (koneksi terputus setelah `COMMIT` terkirim) dicatat sebagai `info` (tak pasti); kegagalan serialisasi (`40001`), *deadlock* (`40P01`), dan galat standby baca-saja (`25006`) dicatat sebagai kegagalan pasti (`fail`).

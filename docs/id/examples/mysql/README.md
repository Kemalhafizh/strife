# Contoh: MySQL 8.4 di bawah Strife

> **Bahasa:** [English](../../../../examples/mysql/README.md) · Bahasa Indonesia

Satu server MySQL 8.4 dengan autentikasi bawaannya, `caching_sha2_password` (Strife mengimplementasikan jalur cepat serta pertukaran penuh melalui TLS ataupun kunci RSA server). Beban kerjanya berupa transaksi list-append; tiap daftar disimpan sebagai kolom dipisahkan koma yang bertambah via `INSERT ... ON DUPLICATE KEY UPDATE`.

```sh
export STRIFE_MYSQL_PASSWORD=$(openssl rand -hex 16)
cd examples/mysql
docker compose up -d

# SERIALIZABLE, diperiksa sebagai strict serializable, saat cgroup server dibekukan
# dan dicairkan kembali (perlu hak root atau sudo tanpa sandi di Docker host):
strife run --config strife.json --expect no-anomaly

# REPEATABLE READ, diperiksa sebagai snapshot isolation, tanpa gangguan:
strife run --config strife-repeatable-read.json --expect violation

docker compose down -v
```

## Yang Diuji oleh CI (job `mysql`)

| Konfigurasi | Isolasi | Diperiksa sebagai | Gangguan | Hasil diharapkan |
|---|---|---|---|---|
| `strife.json` | `SERIALIZABLE` | strict serializable | `freeze` | tanpa anomali (`--expect no-anomaly`) |
| `strife-repeatable-read.json` | `REPEATABLE READ` | snapshot isolation | tidak ada | **pelanggaran** |

`REPEATABLE READ` pada MySQL bukanlah *snapshot isolation*: pembacaan diambil dari snapshot transaksi, tetapi penulisan beroperasi pada baris ter-commit terbaru. Akibatnya, transaksi yang menambahkan elemen ke daftar akan melihat pada pembacaan berikutnya elemen yang di-commit oleh transaksi lain setelah snapshot-nya diambil. Eksekusi selama 30 detik terhadap `mysql:8.4` menemukan 127 pelanggaran konsistensi internal dan 16 siklus G-single (*read skew*) tanpa gangguan apa pun. Hal ini konsisten dengan [analisis Jepsen pada MySQL 8.0.34](https://jepsen.io/analyses/mysql-8.0.34).

`"cgroup": "auto"` meminta transport Docker menemukan cgroup kontainer saat eksekusi dimulai, sehingga konfigurasi tetap portabel antar-host.

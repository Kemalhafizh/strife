# Contoh: TiDB 8.5 di bawah Strife

> **Bahasa:** [English](../../../../examples/tidb/README.md) · Bahasa Indonesia

Satu server TiDB tunggal dengan mesin penyimpanan tersemat `unistore`: cukup untuk menguji lapisan transaksi TiDB (frontend SQL, pengurutan stempel waktu, dan dua mode transaksinya) tanpa deployment PD/TiKV penuh. TiDB memakai protokol MySQL, sehingga Strife menggunakan klien yang sama seperti MySQL.

```sh
cd examples/tidb
docker compose up -d

strife run --config strife-optimistic.json --expect no-anomaly   # mode optimistik, dibekukan & dicairkan
strife run --config strife.json --faults none --expect violation # mode pesimistis (default TiDB)

docker compose down -v
```

## Yang Diuji oleh CI (job `tidb`)

| Konfigurasi | Mode transaksi | Diperiksa sebagai | Gangguan | Hasil diharapkan |
|---|---|---|---|---|
| `strife-optimistic.json` | optimistik (`init_sql`) | snapshot isolation | `freeze` | tanpa anomali (`--expect no-anomaly`) |
| `strife.json` | pesimistis | snapshot isolation | tidak ada | **pelanggaran** |

TiDB mendokumentasikan `REPEATABLE READ`-nya sebagai *snapshot isolation*. Pada mode optimistik, riwayat eksekusi memenuhi kontrak tersebut. Pada mode pesimistis bawaannya, penulisan mengambil kunci dan membaca versi ter-commit terbaru (serupa MySQL), sehingga beban kerja list-append mengamati elemen yang di-commit sesudah snapshot transaksi diambil: Strife melaporkan pelanggaran konsistensi internal serta siklus G-single. TiDB sendiri mendokumentasikan perilaku ini untuk transaksi pesimistis; contoh ini ada untuk membuktikan bahwa checker dapat membedakan kedua mode tersebut.

`db.init_sql` dijalankan di setiap koneksi baru—di sini `SET SESSION tidb_txn_mode = 'optimistic'`.

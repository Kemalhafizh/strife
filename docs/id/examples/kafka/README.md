# Contoh: Kafka (KRaft) di bawah Strife

> **Bahasa:** [English](../../../../examples/kafka/README.md) · Bahasa Indonesia

Tiga broker Kafka dalam mode KRaft (broker dan controller tergabung), dibangun langsung dari arsip rilis Apache (`KAFKA_VERSION`, default 3.9.1; CI juga menjalankan 4.1.0). Klien Strife berbicara dengan protokol Kafka secara langsung: ApiVersions, Metadata v4, Produce v3 dengan `acks=all`, serta Fetch v4 untuk RecordBatch v2 (terverifikasi CRC-32C).

Setiap kunci Strife adalah topik partisi tunggal yang diperlakukan sebagai daftar *append-only*: operasi produce menambahkan satu elemen, operasi baca mengambil keseluruhan log mulai offset 0. Checker list-append lalu menurunkan urutan log dari hasil pembacaan tersebut.

```sh
cd examples/kafka
KAFKA_VERSION=4.1.0 docker compose up -d --build
strife run --config strife.json      # partition, kill -9, pause
docker compose down -v
```

## Yang Diuji oleh CI (job `kafka`, `scripts/live/check-kafka.py`)

- **Tidak ada rekaman yang sudah diakui boleh hilang** (`lost-append`), dan log tidak pernah bercabang, terbalik urutannya, maupun terduplikasi (`incompatible-order`, `duplicate-elements`). Setiap kunci yang memiliki produce terkonfirmasi wajib memiliki pembacaan final yang berhasil.
- **Pembacaan basi ditoleransi.** Kafka tidak menjanjikan pembacaan linearizable: setelah pergantian leader, operasi fetch dapat dilayani oleh leader baru yang *high watermark*-nya belum mengejar, atau oleh leader lama yang terdepak dan belum menyadari bahwa ia telah digantikan. Kedua versi memunculkan pola ini sebagai siklus G-realtime.

Pengujian diperiksa pada level strict serializability sehingga pembacaan basi tersebut terlihat jelas di laporan alih-alih diabaikan diam-diam.

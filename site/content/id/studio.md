---
title: Studio riwayat
description: Periksa history.jsonl dari sistem Anda terhadap linearizability atau level isolasi transaksi, langsung di browser. Berkas tidak diunggah.
eyebrow: Alat di browser
lead: Sudah punya rekaman operasi dari pengujian Anda sendiri? Letakkan berkasnya di sini. Strife memeriksanya dengan checker yang sama dengan `strife check`, lalu menampilkan laporan interaktifnya.
layout: studio
translationKey: studio
empty: Pilih berkas history.jsonl, atau coba salah satu contoh di sebelah kiri.
---

## Format berkas

Satu operasi per baris (JSONL): setiap pemanggilan dicatat sebagai `invoke`, lalu hasilnya sebagai `ok`, `fail`, atau `info` (tidak pasti). Waktu dalam nanodetik sejak awal pengujian.

```json
{"process":1,"type":"invoke","f":"write","key":"k0","value":2,"time":1000000}
{"process":1,"type":"ok","f":"write","key":"k0","value":2,"time":5000000}
{"process":3,"type":"invoke","f":"read","key":"k0","time":9000000}
{"process":3,"type":"ok","f":"read","key":"k0","value":2,"time":12000000}
```

Model `kv` cocok untuk register baca/tulis/compare-and-set. Untuk transaksi multi-kunci, pakai `txn` atau `append` dengan level isolasi yang ingin dibuktikan. Penjelasan setiap model ada di [Consistency Models](/docs/models). Riwayat yang sangat besar lebih cepat diperiksa dengan CLI, karena di browser engine berjalan beberapa kali lebih lambat.

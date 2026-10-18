---
title: Verifikasi bukti uji dan berkas rilis
description: Cek tanda tangan Ed25519 pada bukti uji Strife dan pada berkas rilis, langsung di browser, tanpa memasang apa pun dan tanpa mengunggah berkas.
eyebrow: Untuk auditor
lead: Bukti yang tidak bisa diperiksa sendiri hanyalah klaim. Halaman ini menjalankan `strife verify` di browser Anda, sehingga auditor bisa memastikan sebuah laporan belum diubah sejak ditandatangani.
layout: verify
translationKey: verify
empty: Pilih bundle dan kunci publik di sebelah kiri, lalu tekan “Verifikasi”.
---

## Apa yang diperiksa

**Bukti uji (`bundle.json`).** Pernyataan in-toto v0.1 yang dihasilkan `strife sign` memuat verdict, seed, fingerprint laporan, dan hash SHA-256 dari riwayat operasi. Verifikasi memastikan tanda tangan Ed25519 cocok dengan kunci publik yang Anda pegang. Semua detail yang ditampilkan dibaca dari bagian yang ditandatangani, bukan dari salinan tanpa tanda tangan di dalam berkas. Jika Anda juga memilih `history.jsonl`, halaman ini menghitung ulang hash-nya untuk memastikan riwayat itulah yang ditandatangani.

**Berkas rilis.** Setiap rilis Strife dapat disertai atestasi yang menandatangani hash setiap arsip, `SHA256SUMS`, dan SBOM. Pilih atestasinya beserta berkas yang Anda unduh. Halaman ini mengecek tanda tangannya, lalu mencocokkan hash setiap berkas.

## Dari mana kunci publiknya

Kunci publik harus Anda peroleh lewat jalur yang Anda percayai, misalnya langsung dari tim yang menandatangani laporan, bukan dari berkas yang sama dengan bukti yang diperiksa. Sidik jari kunci ditampilkan di hasil agar bisa dicocokkan lewat telepon atau surat resmi. Kunci rilis resmi Strife diumumkan di halaman [Keamanan](/keamanan).

Verifikasi yang sama tersedia di CLI:

```sh
strife verify --bundle bundle.json --key audit.pub --history history.jsonl
strife verify --bundle strife_v0.2.0.intoto.json --key release.pub --release-dir .
```

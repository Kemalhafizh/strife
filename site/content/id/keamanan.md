---
title: Keamanan & penanganan data
description: Bagaimana Strife menangani data dan kredensial Anda, rantai pasok perangkat lunaknya, kunci rilis, dan cara melaporkan celah keamanan.
eyebrow: Kepercayaan
lead: Jawaban untuk pertanyaan yang biasanya diajukan tim keamanan dan pengadaan sebelum memasang alat baru.
translationKey: security
---

## Data Anda

- **Berjalan di infrastruktur Anda.** CLI dan `strife-agent` berjalan di mesin dan jaringan Anda sendiri. Strife hanya menghubungi target yang Anda konfigurasikan: endpoint database, agen, Docker, SSH, atau Kubernetes. Tidak ada telemetri dan tidak ada koneksi ke server kami.
- **Kredensial target** ditulis sebagai rujukan `env:NAMA` atau `file:PATH`, sehingga tidak pernah tersimpan di berkas konfigurasi maupun laporan.
- **Agen** menolak mendengarkan di luar loopback tanpa token, dan token dibandingkan dalam waktu konstan.
- **Alat di situs ini** (playground, studio, verifikasi) menjalankan engine sebagai WebAssembly di browser Anda. Berkas yang Anda pilih tidak diunggah ke mana pun.
- **Situs ini** tidak memakai cookie maupun pelacak pihak ketiga.

## Rantai pasok

- **Nol dependensi eksternal.** Engine dibangun hanya dari pustaka standar Go, dan `go.mod` tidak memuat blok `require`. Seluruh kode yang berjalan bisa ditinjau di satu repositori.
- **Rilis yang bisa diverifikasi.** Setiap rilis menyertakan arsip untuk Linux, macOS, dan Windows (amd64 dan arm64), `SHA256SUMS`, SBOM SPDX, dan image container multi-arsitektur. Bila kunci rilis sudah dikonfigurasi, rilis juga menyertakan atestasi in-toto yang ditandatangani Ed25519.
- **Fuzzing berkelanjutan** untuk setiap parser data tak tepercaya, yaitu protokol database dan WebAssembly. Temuannya dicatat di [benchmark](/docs/benchmarks#3-checker-correctness).

## Kunci rilis

{{< release-key >}}

Verifikasi berkas yang Anda unduh di [halaman verifikasi](/verifikasi), atau dengan `strife verify --release-dir`.

## Melaporkan celah keamanan

{{< security-contact >}}

## Lisensi

Strife dirilis di bawah [Apache License 2.0](https://github.com/Kemalhafizh/strife/blob/main/LICENSE): Anda boleh memakai, mengubah, dan mendistribusikannya, termasuk untuk keperluan komersial.

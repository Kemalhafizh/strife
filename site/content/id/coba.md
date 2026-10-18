---
title: Coba Strife di browser Anda
description: Jalankan simulator dan checker Strife langsung di browser, tanpa instal. Pilih skenario bug sungguhan, lihat pelanggarannya, lalu ulangi dengan CLI.
eyebrow: Playground
lead: Engine yang sama dengan CLI, dikompilasi ke WebAssembly. Pilih skenario, jalankan, dan bandingkan sidik jari eksekusinya dengan engine native. Tidak ada yang dikirim ke server.
layout: try
translationKey: try
empty: Pilih salah satu skenario di sebelah kiri, atau atur sendiri lalu tekan “Jalankan simulasi”.
---

## Yang sebenarnya terjadi saat Anda menekan tombol

1. Browser Anda menjalankan `strife sim` yang sama dengan rilis CLI: cluster Raft lima node disimulasikan dalam satu proses, dengan jaringan, disk, timer, dan jam yang semuanya dikendalikan oleh seed.
2. Setiap operasi klien direkam, lalu diperiksa secara formal apakah ada urutan yang bisa menjelaskan semua hasilnya (linearizability).
3. Seed yang sama selalu menghasilkan eksekusi yang sama. Untuk skenario siap pakai, halaman ini membandingkan hash eksekusi di browser dengan hasil engine native di Linux saat situs dibangun.

Urutan daftar gangguan ikut menentukan jadwalnya, jadi perintah CLI di hasil memakai urutan persis yang Anda pilih. Untuk menguji sistem Anda sendiri, pakai CLI terhadap cluster staging. Panduannya ada di [Quick Start](/docs/quick-start) dan [Live Clusters](/docs/live-clusters).

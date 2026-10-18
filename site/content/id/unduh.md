---
title: Unduh Strife
description: Unduh CLI Strife untuk Linux, macOS, dan Windows, instal dengan satu perintah, dan pastikan berkasnya asli.
eyebrow: Unduh
lead: Satu berkas program, tanpa dependensi. Gratis dan kode terbuka di bawah lisensi Apache 2.0.
layout: download
translationKey: download
verify_text: Setiap rilis menyertakan `SHA256SUMS`, SBOM SPDX, dan atestasi rilis yang ditandatangani Ed25519. Cek checksum-nya, lalu, kalau perlu kepastian bahwa berkas dibangun oleh kami, verifikasi atestasinya dengan kunci rilis resmi.
---

Butuh Go 1.23 atau lebih baru. Perintah di atas mengambil kode langsung dari repositori dan mengompilasinya di mesin Anda. Tidak ada dependensi pihak ketiga yang ikut diunduh.

## Kebutuhan sistem

- **Simulasi, pemeriksaan riwayat, shrink, dan verifikasi** berjalan di Linux, macOS, dan Windows (amd64 dan arm64).
- **Uji cluster sungguhan.** CLI menyuntikkan gangguan ke node target lewat Docker, SSH, Kubernetes (`kubectl exec`), atau `strife-agent`. Target dan agennya butuh Linux. Partisi jaringan memakai iptables dan `tc netem`, sedangkan freeze serta I/O stall memakai cgroup v2.
- Langkah selanjutnya ada di [Quick Start](/docs/quick-start), dan contoh konfigurasi untuk etcd, PostgreSQL, MySQL/TiDB, Valkey, Kafka, dan Kubernetes ada di [dokumentasi](/docs).

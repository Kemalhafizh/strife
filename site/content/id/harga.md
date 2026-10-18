---
title: Engine-nya gratis. Pendampingannya yang berbayar.
description: Engine Strife gratis dan terbuka di bawah Apache 2.0. Paket berbayar mencakup pendampingan uji di staging, dossier kepatuhan, dan dukungan dengan SLA.
eyebrow: Harga
lead: Seluruh engine (simulator, checker, agen, CLI) berlisensi Apache 2.0 dan akan tetap terbuka. Yang kami tawarkan adalah keahlian untuk menjalankannya di sistem Anda dan membawa hasilnya ke ruang audit.
layout: pricing
translationKey: pricing
plans:
  - name: Community
    price: Gratis
    note: Selamanya, kode terbuka (Apache 2.0)
    features:
      - Simulator deterministik, checker, shrink, dan kampanye regresi
      - "Uji cluster sungguhan: etcd, PostgreSQL, MySQL/TiDB, Valkey, Kafka, Kubernetes"
      - strife-agent, GitHub Action, laporan HTML interaktif
      - Tanda tangan dan dossier kepatuhan dengan kunci Anda sendiri
      - Dukungan komunitas
    cta: Unduh
    link: /unduh
  - name: Assurance
    price: Sesuai cakupan
    note: Kontrak tahunan, untuk satu tim dan lingkungan staging-nya
    featured: true
    features:
      - Pendampingan menyiapkan uji di staging Anda
      - Skenario uji untuk arsitektur Anda (database, replikasi, antrean pesan)
      - Tinjauan temuan bersama engineer kami, termasuk akar masalahnya
      - Dossier kepatuhan yang disusun dan ditinjau bersama untuk audit Anda
      - Dukungan dengan SLA pada jam kerja
    cta: Jadwalkan demo
    link: /kontak
  - name: Enterprise
    price: Hubungi kami
    note: Untuk beberapa tim, lingkungan, atau sistem inti
    features:
      - Semua yang ada di Assurance
      - Klien protokol dan adaptor penyimpanan khusus untuk sistem internal
      - Gerbang regresi di pipeline rilis (CI/CD)
      - Runner terkelola di dalam jaringan Anda <span class="soon">dalam pengembangan</span>
      - Lokakarya on-site untuk tim engineering dan risiko
    cta: Hubungi kami
    link: /kontak
---

## Pertanyaan yang sering diajukan

### Apakah Strife benar-benar gratis?

Ya. Engine lengkapnya berlisensi Apache 2.0. Anda boleh memakainya, mengubahnya, dan menjalankannya di sistem produksi tanpa membayar. Yang berbayar adalah pendampingan, dossier, dan dukungan.

### Bagaimana proses pembeliannya?

Lewat penawaran tertulis dan invoice, mengikuti proses pengadaan perusahaan Anda. Tidak perlu kartu kredit.

### Berapa harganya?

Tergantung jumlah sistem dan lingkungan yang diuji. Kami mengirim penawaran setelah diskusi awal, yang gratis dan tanpa kewajiban apa pun.

### Apakah data kami dikirim ke Strife?

Tidak. Pengujian berjalan di infrastruktur Anda, dan kami bekerja bersama tim Anda di lingkungan Anda. Detailnya ada di halaman [Keamanan & data](/keamanan).

### Apakah dossier menjamin kami lulus audit?

Tidak. Dossier adalah bukti teknis untuk auditor, bukan sertifikasi. Batasannya kami tuliskan di halaman [Kepatuhan](/kepatuhan).

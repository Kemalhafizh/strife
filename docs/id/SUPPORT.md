# Dukungan & Komunitas

> **Bahasa:** [English](../../SUPPORT.md) · Bahasa Indonesia

**Strife** adalah proyek pengujian dan verifikasi sistem terdistribusi kode terbuka yang dibuat dan dipelihara oleh [Muhammad Kemal Hafizh](https://github.com/Kemalhafizh) di bawah lisensi [Apache License 2.0](../../LICENSE).

## Dokumentasi & Instrumen Interaktif

Sebelum membuka *issue*, periksa dokumentasi, catatan rilis, dan perkakas berbasis browser berikut:

- **Dokumentasi & Referensi CLI:** [https://kemalhafizh.github.io/strife/docs/](https://kemalhafizh.github.io/strife/docs/)
- **Playground WebAssembly Interaktif:** [https://kemalhafizh.github.io/strife/coba/](https://kemalhafizh.github.io/strife/coba/)
- **Studio Timeline & Contoh Tandingan:** [https://kemalhafizh.github.io/strife/studio/](https://kemalhafizh.github.io/strife/studio/)
- **Verifikator Atestasi Sisi Klien:** [https://kemalhafizh.github.io/strife/verifikasi/](https://kemalhafizh.github.io/strife/verifikasi/)
- **Registri Run Self-Hosted (`strife serve`):** [`docs/id/server.md`](server.md)
- **GitHub Action & Resep CI:** [`docs/id/github-action.md`](github-action.md)
- **Tolok Ukur & Validasi Empiris:** [`docs/id/BENCHMARKS.md`](BENCHMARKS.md)
- **Catatan Rilis:** [`docs/id/CHANGELOG.md`](CHANGELOG.md) · **Tata Kelola:** [`docs/id/GOVERNANCE.md`](GOVERNANCE.md)

## Tips Pemecahan Masalah

- **Verdict `unknown` (`kode keluar 3`):** Checker kehabisan `--max-steps` atau `--check-timeout`, atau eksekusi cluster sungguhan mencatat nol operasi berhasil (`OK`). Jalankan ulang `strife check` dengan `--max-steps` lebih besar, perkecil jejaknya lewat `strife shrink --history`, atau jalankan `strife doctor --config strife.json` untuk memeriksa keterjangkauan cluster.
- **Izin injeksi gangguan:** Bila gangguan `docker` atau `cgroup v2` tidak tersedia di host Anda, jalankan `strife doctor` dan pertimbangkan transport `proxy` ruang pengguna (`transport.type: "proxy"`).

## Mendapatkan Bantuan & Berpartisipasi

- **Pertanyaan & Bantuan Penggunaan:** Buka [Question / Discussion issue](https://github.com/Kemalhafizh/strife/issues/new?template=question.yml) apabila Anda membutuhkan bantuan mengonfigurasi target cluster, menyusun beban kerja, atau membaca saksi contoh tandingan.
- **Laporan Bug:** Buka [Bug Report](https://github.com/Kemalhafizh/strife/issues/new?template=bug_report.yml) dengan mencantumkan perintah persis, `--seed`, `schedule.json`, atau berkas jejak `history.jsonl` / `.edn`.
- **Permintaan Fitur & Model Baru:** Buka [Feature Request](https://github.com/Kemalhafizh/strife/issues/new?template=feature_request.yml) atau kirimkan *pull request* sesuai [`CONTRIBUTING.md`](CONTRIBUTING.md).
- **Kerentanan Keamanan:** Laporkan isu keamanan secara privat melalui [`SECURITY.md`](SECURITY.md).

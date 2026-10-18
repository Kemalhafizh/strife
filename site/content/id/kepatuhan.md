---
title: Bukti uji ketahanan untuk audit TI bank dan fintech
description: Strife memetakan hasil uji gangguan ke kontrol yang mengutip POJK 11/2022, SEOJK 29/2022, DORA, SOC 2, dan ISO 27001, lalu mengikatnya dengan tanda tangan Ed25519 yang bisa diverifikasi siapa saja.
eyebrow: Kepatuhan & audit
lead: "`strife compliance` mengubah hasil uji gangguan menjadi dossier yang memetakan setiap temuan ke kontrol yang mengutip regulasi, lengkap dengan bukti, celah, dan batasan metodologinya."
translationKey: compliance
---

## Yang dihasilkan

- **Dossier siap cetak** (HTML, Markdown, dan JSON) dari satu laporan uji atau dari kampanye banyak seed.
- **Setiap kontrol dinilai dari bukti empiris:** LULUS, PARSIAL (cakupan gangguan atau penandatanganan belum lengkap), atau GAGAL. Dossier juga menyebutkan celahnya dan cara menutupnya.
- **Bagian “Metodologi & Batasan Cakupan”** di setiap dossier: apa yang diuji, jenis gangguan yang tidak dicakup, dan bedanya simulasi deterministik dengan uji pada cluster sungguhan.
- **Tanda tangan kriptografis.** Laporan ditandatangani dengan Ed25519 (in-toto v0.1) memakai kunci milik tim Anda, dan siapa pun bisa memverifikasinya ulang, termasuk lewat [halaman verifikasi](/verifikasi) tanpa memasang apa pun.

## Kontrol untuk regulasi Indonesia

ID kontrol dan rujukan ketentuannya di bawah ini dibaca langsung dari engine, sehingga selalu sama dengan isi dossier.

{{< controls framework="bi-ojk" >}}

## Kerangka lain

{{< controls framework="other" >}}

## Alurnya

1. **Uji.** Jalankan `strife sim` untuk simulasi, `strife run` terhadap cluster staging, atau `strife campaign` untuk banyak seed sekaligus.
2. **Tanda tangani.** Buat pasangan kunci untuk tim QA atau audit dengan `strife keygen`, lalu tandatangani laporan dengan `strife sign`.
3. **Susun dossier.** Jalankan `strife compliance` dengan kerangka yang Anda perlukan.
4. **Verifikasi.** Auditor memeriksa tanda tangan dengan `strife verify` atau di browser.

```sh
strife compliance --report strife-out/raft-seed7/report.json \
  --bundle strife-out/raft-seed7/bundle.json --key audit-key.pub \
  --frameworks dora,soc2,iso27001,bi-ojk --out strife-compliance --strict
```

Lihat [contoh dossier](/demos/dossier.html) (data simulasi) dan panduan lengkapnya di [dokumentasi atestasi](/docs/attestation).

## Batasan yang kami nyatakan terang-terangan

> [!IMPORTANT]
> Dossier adalah **bukti teknis untuk auditor, bukan sertifikasi, bukan opini hukum, dan bukan pernyataan dari OJK**. ID kontrol adalah label Strife untuk pemeriksaan teknisnya, bukan nomor yang diterbitkan regulator. Setiap kontrol menyebutkan ketentuan yang didukungnya, dan pemetaannya bersifat informatif.

- **Simulasi** membuktikan perilaku algoritma secara deterministik dan bisa diputar ulang bit per bit. Namun yang diuji adalah model sistemnya, bukan sistem produksi Anda.
- **Uji cluster sungguhan** memberi bukti tentang sistem Anda, tetapi tidak bisa diputar ulang persis sama: jadwal gangguan dan beban kerjanya identik, sedangkan waktu eksekusinya tidak. Dossier menyebutkan mode mana yang dipakai.
- **Tidak menemukan pelanggaran bukan bukti bahwa sistem bebas bug.** Artinya, dalam cakupan gangguan dan durasi yang diuji, tidak ada pelanggaran yang ditemukan. Cakupan itu tertulis di dossier.

## Bawa ke audit berikutnya

Kami membuka program design partner terbatas untuk bank dan fintech di Indonesia yang ingin menjalankan Strife di staging dan membawa dossier pertamanya ke audit sungguhan. [Hubungi kami](/kontak).

# Strife di CI

> **Bahasa:** [English](../github-action.md) · Bahasa Indonesia

Strife terintegrasi ke sistem CI dalam dua lapisan:

1. **Format laporan** yang dipahami oleh setiap platform CI. Tersedia pada perintah `sim`, `campaign`, `shrink`, `check`, `run`, dan `compliance`:
   - `--junit FILE` menulis laporan JUnit XML.
   - `--sarif FILE` menulis laporan SARIF 2.1.0 untuk GitHub Code Scanning.
   - `--summary FILE` menambahkan ringkasan Markdown.
2. **GitHub Action** ([`action.yml`](../../action.yml) di akar repositori ini) yang mengunduh biner rilis terverifikasi checksum saat dipatok ke tag rilis (atau membangun dari kode sumber), menjalankan Strife, lalu melakukan hal berikut:
   - menerbitkan ringkasan pada halaman ringkasan job,
   - mengunggah laporan HTML/JSON, JUnit XML, dan berkas SARIF 2.1.0 sebagai artefak,
   - secara opsional mendorong laporan atau kampanye ke registri `strife serve` saat menjalankan `sim` (satu seed), `campaign`, `run`, atau `check`,
   - memberi anotasi pada eksekusi,
   - menggagalkan job ketika Strife menemukan masalah.

## GitHub Action

```yaml
name: consistency
on: [push, pull_request]

jobs:
  strife:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: kemalhafizh/strife@v1.0.0   # patok ke tag rilis atau SHA commit
        with:
          command: sim --system raft --seeds 1..500 --determinism --expect linearizable
```

### Input

| Input | Default | Keterangan |
| --- | --- | --- |
| `command` | (wajib) | Baris perintah strife tanpa kata depan `strife`. Dipecah berdasarkan spasi dan tidak pernah dievaluasi oleh shell, sehingga tanda kutip dan glob tidak berlaku. |
| `out` | `strife-out` | Direktori laporan (`""` untuk menonaktifkan). |
| `junit` | `strife-junit.xml` | Jalur berkas JUnit XML (`""` untuk menonaktifkan). |
| `sarif` | `""` | Jalur laporan SARIF 2.1.0 (`""` untuk menonaktifkan). |
| `summary` | `true` | Tambahkan ringkasan Markdown ke job summary. |
| `push` | `""` | URL registri yang diteruskan lewat `--push` untuk `sim` (satu seed), `campaign`, `run`, dan `check`. Penyapuan multi-seed (`sim --seeds`) tidak mendukung `--push`; gunakan `campaign` atau lihat [`docs/id/server.md`](server.md#menerbitkan-dari-ci). |
| `push-token` | `""` | Token Bearer opsional untuk `--push-token`. |
| `tenant` | `default` | Pengenal tenant untuk `--tenant`. |
| `upload` | `true` | Unggah `out`, `junit`, dan `sarif` sebagai artefak. Pengunggahan tetap berjalan meski Strife gagal. |
| `artifact-name` | `strife-reports` | Nama artefak. Harus unik per workflow bila Action dipanggil lebih dari sekali. |
| `fail` | `true` | Gagalkan langkah saat Strife keluar dengan kode selain nol. |
| `go-version` | `go.mod` Strife | Versi Go saat membangun Strife dari sumber. |

### Output

| Output | Keterangan |
| --- | --- |
| `exit-code` | `0` lulus, `1` pelanggaran, `2` galat penggunaan, `3` tak pasti (`inconclusive`), `4` galat eksekusi |
| `outcome` | `passed`, `violation`, `inconclusive`, `usage-error`, atau `error` |
| `junit` | Jalur berkas JUnit |
| `sarif` | Jalur berkas SARIF 2.1.0 (bila diaktifkan) |
| `report-dir` | Jalur direktori laporan |

### Resep Penggunaan

**Gerbang regresi.** Setiap seed sistem yang benar wajib tetap linearizable, dan setiap pemutaran ulang wajib identik:

```yaml
- uses: kemalhafizh/strife@v1.0.0
  with:
    command: sim --system raft --seeds 1..1000 --determinism --expect linearizable
```

**Perburuan bug yang harus menemukan pelanggaran.** Pengujian dinyatakan lulus ketika setidaknya satu seed melanggar. `--shrink` melampirkan pereproduksi minimal ke laporan:

```yaml
- uses: kemalhafizh/strife@v1.0.0
  with:
    command: sim --system raft-unsafe-fsync --faults kill,io-stall --seeds 1..300 --expect violation --shrink
```

**Gerbang kampanye multi-seed dengan perbandingan regresi baseline:**

```yaml
- uses: kemalhafizh/strife@v1.0.0
  with:
    command: campaign --system raft --seeds 1..200 --baseline baseline.json --expect no-regression
```

**Periksa jejak Strife `.jsonl`, Jepsen `.edn`, atau OpenTelemetry Span `.json` yang direkam oleh suite uji Anda sendiri:**

```yaml
- run: ./run-my-test-suite --otel-out traces.json
- uses: kemalhafizh/strife@v1.0.0
  with:
    command: check --model kv --max-mttr 500ms --min-availability 0.99 traces.json
```

**Hasilkan dossier kepatuhan (`dora,soc2,iso27001,bi-ojk,pci-dss,bi-sp`) lengkap dengan gerbang SLA dan aturan SARIF:**

```yaml
- uses: kemalhafizh/strife@v1.0.0
  with:
    command: sim --system outbox-settlement --seed 7 --expect linearizable
    out: settle-out
- uses: kemalhafizh/strife@v1.0.0
  with:
    command: compliance --report settle-out/outbox-settlement-seed7/report.json --frameworks dora,soc2,iso27001,bi-ojk,pci-dss,bi-sp --max-mttr 500ms --min-availability 0.95
    out: settle-compliance
    sarif: compliance.sarif.json
```

**Gerbang & Ringkasan Komparatif Pull Request (`strife ci-report`):**

```yaml
- run: |
    strife ci-report \
      --report candidate/report.json \
      --baseline-report baseline/report.json \
      --dossier settle-compliance/dossier.json \
      --max-mttr 500ms --min-availability 0.99 \
      --out "$GITHUB_STEP_SUMMARY" --json pr-verdict.json
```

**Tanggapi verdict secara terprogram:**

```yaml
- id: strife
  uses: kemalhafizh/strife@v1.0.0
  with:
    command: sim --system raft-stale-reads --faults partition --seeds 1..50
    fail: "false"
- if: steps.strife.outputs.outcome == 'violation'
  run: echo "found one, see ${{ steps.strife.outputs.report-dir }}"
```

**Tampilkan hasil di tab Checks pada PR.** Teruskan berkas JUnit ke Action pelapor JUnit mana saja, misalnya:

```yaml
- uses: mikepenz/action-junit-report@v4
  if: always()
  with:
    report_paths: strife-junit.xml
```

**Publikasikan temuan SARIF 2.1.0 ke GitHub Code Scanning:**

```yaml
- uses: kemalhafizh/strife@v1.0.0
  with:
    command: sim --system outbox-settlement --seeds 1..100 --expect linearizable
    sarif: strife.sarif.json
- uses: github/codeql-action/upload-sarif@v3
  if: always()
  with:
    sarif_file: strife.sarif.json
```

> [!NOTE]
> `strife sim` (satu `--seed`), `strife campaign`, `strife run`, dan `strife check` semuanya mendukung `--push ... --push-token ... --tenant ...` langsung dari CLI. Untuk penyapuan multi-seed `sim --seeds` atau artefak `report.json` / `campaign.json` yang sudah ada, kirim JSON langsung ke `/api/v1/ingest` seperti dijelaskan di [`docs/id/server.md`](server.md#menerbitkan-dari-ci).

## Isi Laporan JUnit

Elemen `testcase` bergantung pada perintah yang dijalankan:

| Perintah | Elemen `testcase` | Arti kegagalan |
| --- | --- | --- |
| `sim --seed N`, `shrink`, `check` | satu per kunci di dalam riwayat | riwayat kunci tersebut melanggar model; tubuh XML memuat contoh tandingan dan perintah reproduksinya |
| `sim --seeds A..B` | satu per seed | seed tersebut melanggar; tubuh memuat penjelasannya dan perintah reproduksi |
| `sim --seeds A..B --expect violation` | satu: `finds a violation` | tidak ada seed dalam rentang tersebut yang melanggar (menemukan bug *adalah* tujuan uji) |
| `sim --seed N --expect …` | satu: `expect …` | verdict berbeda dari ekspektasi |
| `campaign` | satu per seed (`seed N`), atau `finds a violation` dengan `--expect violation` | seed tersebut melanggar atau mengalami regresi; di bawah `--expect no-regression`, kegagalan baseline yang sudah diketahui dicatat sebagai `skipped` |
| `run` | satu per kunci, ditambah `fault injection` | sama seperti di atas; `fault injection` bernilai error bila perintah gangguan gagal, sehingga cluster tidak menerima jadwal yang direncanakan |
| `compliance` | satu per kontrol yang dievaluasi (`[FW] control_id — Judul`) | kontrol dinilai `FAIL` (atau `PARTIAL` saat `--strict` diaktifkan) |

Pemetaan status eksekusi:

- Pemeriksaan yang tidak konklusif (`unknown`, anggaran pencarian habis) menjadi `error` JUnit, bukan lulus.
- Eksekusi yang sama sekali tidak bisa diselesaikan (konfigurasi salah, cluster tak terjangkau, terdeteksi nondeterminisme) dilaporkan sebagai satu kasus `error`, sehingga CI tidak pernah menampilkan hijau untuk uji yang sebenarnya gagal berjalan.

Atribut `properties` suite membawa jejak audit lengkap:

- versi Strife,
- seed (atau jumlah seed & jumlah jejak kampanye),
- verdict (atau status keseluruhan kepatuhan),
- sidik jari SHA-256 laporan, kampanye, atau dossier,
- SHA-256 dari jadwal, riwayat, dan jejak (untuk eksekusi simulasi maupun langsung),
- perintah reproduksi.

## Sistem CI Lainnya

Flag laporan menghasilkan berkas standar biasa, sehingga dapat dipakai di sistem CI apa pun:

```sh
strife sim --system raft --seeds 1..500 --expect linearizable \
  --junit strife-junit.xml --sarif strife.sarif.json --summary strife-summary.md
```

- **GitLab:** `artifacts: reports: junit: strife-junit.xml`
- **Jenkins:** `junit 'strife-junit.xml'`
- **CircleCI:** `store_test_results`
- **Azure Pipelines:** `PublishTestResults@2`
- **Buildkite:** plugin test-collector

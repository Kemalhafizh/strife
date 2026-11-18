# Registri Run (`strife serve`)

> **Bahasa:** [English](../server.md) · Bahasa Indonesia

`strife serve` menjalankan daemon HTTP *self-hosted* yang menyimpan laporan satu-seed (`report.json`) dan kampanye banyak-seed (`campaign.json`) di dalam direktori *append-only* di jaringan Anda sendiri. Saat diterima, setiap laporan atau kampanye dihitung ulang sidik jari SHA-256-nya, kampanye otomatis dibandingkan terhadap baseline sebelumnya untuk pasangan `(tenant, target)` yang sama, dan laporan dapat ditandatangani otomatis menjadi bundel Ed25519 `in-toto` v0.1.

Daemon ini tertanam langsung di dalam biner `strife` tanpa dependensi eksternal dan menyajikan baik REST API JSON maupun dasbor web bertema gelap tersemat.

> [!NOTE]
> `strife serve` berkomunikasi lewat HTTP teks polos (*plaintext*, default `127.0.0.1:8080`). Saat mengeksposnya melampaui loopback, letakkan di belakang *reverse proxy* yang mengakhiri TLS lalu aktifkan `--token` dan/atau `--tenant-tokens`.

## Menjalankan Server

```sh
# Dengarkan di localhost dengan direktori penyimpanan default (./strife-registry)
strife serve

# Konfigurasi produksi / staging dengan token admin + tenant, lease pekerjaan, kunci Ed25519 per tenant, gerbang SLA, dan peringatan webhook
strife keygen --out registry-key
strife keygen --out tenant-payments
strife serve \
  --listen 127.0.0.1:8080 \
  --store /var/lib/strife/registry \
  --token "env:STRIFE_REGISTRY_TOKEN" \
  --tenant-tokens "payments=env:STRIFE_PAYMENTS_TOKEN" \
  --job-lease 2m \
  --key registry-key.key \
  --tenant-keys "payments=tenant-payments.key" \
  --signer "staging-run-registry" \
  --max-mttr 500ms \
  --min-availability 0.99 \
  --webhook "https://hooks.slack.com/services/T00/B00/X00,https://discord.com/api/webhooks/123/abc"
```

### Flag Perintah

| Flag | Default | Deskripsi |
| :--- | :--- | :--- |
| `--listen` | `127.0.0.1:8080` | Alamat TCP untuk server HTTP dan antarmuka web |
| `--store` | `strife-registry` | Direktori untuk indeks registri *append-only* dan artefak run |
| `--token` | *(kosong)* | Token Bearer admin opsional yang wajib disertakan pada antarmuka web (`/`) maupun endpoint API (`/api/v1/runs*`, `/api/v1/series`, `/api/v1/trends`, `/api/v1/webhooks`, `/api/v1/jobs*`, `/api/v1/ingest`), dibandingkan dalam waktu konstan (mendukung `env:VAR` atau `file:PATH`) |
| `--tenant-tokens` | *(kosong)* | Pasangan `tenant=token` opsional dipisahkan koma untuk autentikasi Bearer terlingkup tenant (`token` mendukung `env:VAR` atau `file:PATH`; tiap token harus unik dan terisolasi per tenant dengan `403 Forbidden` pada permintaan lintas tenant atau `/api/v1/webhooks`) |
| `--job-lease` | `2m` | Durasi lease sebelum pekerjaan worker berstatus `claimed` yang tidak diperbarui dapat diambil ulang |
| `--key` | *(kosong)* | Kunci privat Ed25519 PKCS#8 default (`.key`) opsional untuk menandatangani laporan yang masuk |
| `--tenant-keys` | *(kosong)* | Pasangan `tenant=path.key` opsional dipisahkan koma untuk penandatanganan Ed25519 per tenant (`signer:<tenant>`) |
| `--signer` | `strife-serve` | Identitas penanda tangan yang dicatat di dalam atestasi `in-toto` otomatis |
| `--webhook` | *(kosong)* | URL webhook dipisahkan koma (Slack, Discord, atau JSON generik) yang diberitahu saat terjadi pelanggaran, regresi, dan pelanggaran SLA |
| `--max-mttr` | `0s` | Ambang batas opsional P95 MTTR pasca-pemulihan (mis. `500ms`) yang memicu peringatan webhook `report.sla_breach` |
| `--min-availability` | `0` | Ambang minimum ketersediaan opsional dalam rasio `[0,1]` atau persentase `(1,100]` (mis. `0.99` atau `99`) yang memicu peringatan webhook `report.sla_breach` |

> [!IMPORTANT]
> Ketika `--token` atau `--tenant-tokens` dikonfigurasi, autentikasi bersifat ***fail-closed***: `/`, `/api/v1/runs*`, `/api/v1/series`, `/api/v1/trends`, `/api/v1/webhooks`, `/api/v1/jobs*`, dan `/api/v1/ingest` seluruhnya mewajibkan `Authorization: Bearer <token>` (`401 Unauthorized` jika absen/salah). Hanya `GET /api/v1/keys` (kunci verifikasi publik) serta `GET /api/v1/health` yang disaring (*redacted*) yang dapat diakses tanpa token.

## Tata Letak Penyimpanan di Disk

Direktori penyimpanan mudah diperiksa secara langsung oleh manusia dan didukung oleh indeks JSON dipisahkan baris baru (`registry.jsonl`) beserta folder artefak per run:

```text
strife-registry/
├── registry.jsonl                 # Indeks append-only berisi seluruh run dan kampanye yang masuk
└── runs/
    ├── run-0001-a1b2c3d4/
    │   ├── report.json            # Laporan satu-seed yang telah diverifikasi
    │   └── bundle.json            # Tersedia bila --key atau --tenant-keys dikonfigurasi (bundel Ed25519 in-toto v0.1)
    └── camp-0002-e5f60718/
        └── campaign.json          # Manifes kampanye multi-seed (dengan Comparison baseline terlampir)
```

Ketika `strife serve` dinyalakan ulang, daemon memutar ulang `registry.jsonl` dan memuat kembali kampanye terbaru per `(tenant, target)` ke dalam memori sehingga pelacakan regresi baseline berlanjut melintasi proses *restart*.

## Referensi HTTP API

### `GET /api/v1/health`

Mengembalikan status server, versi engine, lama waktu aktif (*uptime*), status penandatanganan, ambang SLA yang terkonfigurasi (jika bukan nol), jumlah webhook, dan status autentikasi (`auth_enabled`). Bila terautentikasi (atau bila token tidak dikonfigurasi), respons juga menyertakan `stored_runs`, `targets`, dan `tenants` sesuai cakupan pemanggil; pemanggil tanpa autentikasi menerima versi ringkas tanpa bidang run/target/tenant tersebut.

```sh
curl -fsSL -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" http://127.0.0.1:8080/api/v1/health
```

```json
{
  "auth_enabled": true,
  "key_id": "9f86d081884c7d65",
  "signing_active": true,
  "sla_max_mttr_ms": 500,
  "sla_min_availability": 0.99,
  "status": "ok",
  "stored_runs": 14,
  "targets": ["etcd-cluster", "raft", "raft-ledger"],
  "tenant_keys": 1,
  "tenants": ["default", "payments"],
  "uptime_seconds": 3620,
  "version": "1.0.0",
  "webhooks_configured": 2
}
```

### `GET /api/v1/keys`

Mengembalikan seluruh kunci verifikasi publik Ed25519 yang aktif (`default` dari `--key` ditambah kunci per tenant dari `--tenant-keys`) dalam format PEM PKIX (`public_key_pem`, `key_id`, `tenant`, `signer`) sehingga auditor dan pipeline CI dapat memverifikasi artefak `bundle.json` secara independen.

```sh
curl -fsSL http://127.0.0.1:8080/api/v1/keys
```

### `GET /api/v1/webhooks`

Endpoint khusus admin (mewajibkan token Bearer admin `--token` ketika autentikasi aktif; token terlingkup tenant ditolak dengan `403 Forbidden`). Mengembalikan jumlah endpoint webhook terkonfigurasi serta log pengiriman terbaru (`run_id`, `event`, `format`, `url` teredaksi, `status_code`, `success`, `duration_ms`). Token webhook, kredensial query string, dan rahasia jalur Slack/Discord otomatis disamarkan sebelum dikembalikan.

```sh
curl -fsSL -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" http://127.0.0.1:8080/api/v1/webhooks
```

### `GET /api/v1/runs`

Mendaftar eksekusi terindeks dengan urutan terbaru terlebih dahulu (mewajibkan autentikasi Bearer saat `--token` atau `--tenant-tokens` aktif; token terlingkup tenant dibatasi hanya pada tenant miliknya dan menerima `403 Forbidden` jika meminta tenant lain). Mendukung parameter kueri opsional:

- `tenant=<nama>` — saring berdasarkan namespace tenant (default mencocokkan semua tenant bagi token admin, atau tenant milik token untuk token terlingkup tenant).
- `target=<nama>` — saring berdasarkan nama sistem atau cluster (misalnya `raft` atau `postgres-ha`).
- `kind=<report|campaign>` — saring berdasarkan jenis artefak.

```sh
curl -fsSL -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" "http://127.0.0.1:8080/api/v1/runs?tenant=payments&target=raft&kind=campaign"
```

### `GET /api/v1/runs/<id>`

Mengembalikan metadata indeks `entry` bersama artefak JSON mentah yang tersimpan (`report` atau `campaign`, serta `bundle` apabila bertanda tangan). Mewajibkan autentikasi Bearer serta kepemilikan tenant (`403 Forbidden` antar tenant).

```sh
curl -fsSL -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" http://127.0.0.1:8080/api/v1/runs/run-0001-a1b2c3d4
```

### `GET /api/v1/runs/<id>/dossier`

Mengevaluasi dan mengembalikan dossier kepatuhan regulasi (`dora`, `soc2`, `iso27001`, `bi-ojk`, `pci-dss`, `bi-sp`) untuk eksekusi atau kampanye tersimpan tersebut (mewajibkan autentikasi Bearer serta kepemilikan tenant ketika autentikasi aktif). Bila eksekusi ditandatangani dengan Ed25519 (`bundle.json`), tanda tangannya diverifikasi memakai kunci penandatangan tenant tersebut (jatuh ke `--key` default) lalu dicatat di dalam dossier.

- `?frameworks=dora,bi-ojk,pci-dss,bi-sp` — saring kerangka yang dievaluasi (default `all`)
- `?format=html` — render dossier kepatuhan HTML5 mandiri siap cetak alih-alih JSON (dipilih otomatis juga saat mengirim header `Accept: text/html`)
- `?max_mttr=<durasi>` — timpa gerbang SLA `--max-mttr` server untuk evaluasi ini (misalnya `500ms`)
- `?min_availability=<nilai>` — timpa gerbang SLA `--min-availability` server menggunakan rasio di `[0,1]` atau persentase di `(1,100]` (misalnya `0.99` atau `99`)

```sh
curl -fsSL -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" "http://127.0.0.1:8080/api/v1/runs/run-0001-a1b2c3d4/dossier?frameworks=dora,bi-ojk,pci-dss,bi-sp&max_mttr=500ms"
curl -fsSL -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" "http://127.0.0.1:8080/api/v1/runs/run-0001-a1b2c3d4/dossier?format=html" -o dossier.html
```

### `GET /api/v1/series` & `GET /api/v1/trends`

`GET /api/v1/series` mengembalikan agregasi deret waktu keandalan dan rekap MTTR pasca-pemulihan per `(tenant, target)` melintasi seluruh eksekusi dan kampanye tersimpan (`pass_rate`, `avg_availability`, `latest_mttr_p95_ms`, `max_mttr_p95_ms`, dan `points` kronologis).

`GET /api/v1/trends` mengembalikan `{target, tenant?, total, points}` berisi titik tren ketahanan berurutan kronologis (terlama terlebih dahulu: `id`, `tenant`, `kind`, `target`, `model`, `commit`, `branch`, `timestamp`, `verdict`, `passed`, `violations`, `regressions`, `overall_availability`, `fault_availability`, `mttr_p95_ms`, `mttr_max_ms`). Kedua endpoint menegakkan autentikasi Bearer serta penyaringan per tenant (`?tenant=<nama>` dan `?target=<nama>`).

```sh
curl -fsSL -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" "http://127.0.0.1:8080/api/v1/series?tenant=default"
curl -fsSL -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" "http://127.0.0.1:8080/api/v1/trends?tenant=payments&target=outbox-settlement"
```

### Antrean Worker Jarak Jauh Berbasis Pull (`/api/v1/jobs` & `strife worker`)

Untuk VPC privat dan jaringan staging terisolasi yang tidak dapat membuka port masuk menuju control plane, `strife serve` menyediakan antrean tugas berbasis *pull* yang dipersistenkan secara atomik di `jobs.json` (ditulis via berkas sementara `fsync` + *rename*, izin `0600`):

- **`POST /api/v1/jobs`** — Masukkan tugas `sim`, `campaign`, atau `run` ke antrean (`kind`, `system`, `seed` / `seeds`, `nodes`, `clients`, `duration`, `faults`, atau `live_config`, ditambah `tenant`, `commit`, `branch` opsional; dibatasi maksimal 4 MiB). Menegakkan cakupan tenant pemanggil (`403 Forbidden` bila token tenant menargetkan tenant lain).
- **`GET /api/v1/jobs`** & **`GET /api/v1/jobs/<id>`** — Daftar atau periksa tugas yang sedang mengantre, diambil, selesai, atau gagal (dapat disaring dengan `?tenant=` dan `?status=`). Mewajibkan autentikasi Bearer saat `--token` atau `--tenant-tokens` aktif, menyaring ke tenant pemanggil (`403 Forbidden` antar tenant), serta menghapus nilai rahasia `claim_token` dari respons.
- **`POST /api/v1/jobs/claim`** — Mengambil secara atomik tugas `pending` tertua—atau tugas `claimed` yang batas `lease_expires_at`-nya telah lewat—(opsional disaring oleh `tenant`) untuk `worker_id` jarak jauh, sekaligus merekonsiliasi dan melewati tugas kedaluwarsa yang `job_id`-nya sudah terkomit di `Store`. Mengembalikan `claim_token` per klaim beserta `lease_expires_at` (`--job-lease`, default `2m`) dan menaikkan `attempts`.
- **`POST /api/v1/jobs/<id>/heartbeat`** — Memperpanjang lease tugas yang sedang diklaim dengan mengirim `{"worker_id":"...","claim_token":"..."}` (`409 Conflict` bila lease sudah kedaluwarsa, tugas tidak lagi berstatus `claimed`, penyelesaian sedang berjalan, atau `worker_id`/`claim_token` tidak cocok). `strife worker` otomatis mengirimkan heartbeat latar belakang setiap ~1/3 durasi lease (dibatasi `[25ms, 30s]`) selama mengeksekusi tugas.
- **`POST /api/v1/jobs/<id>/complete`** — Mengirimkan `report` atau `campaign` yang selesai bersama `worker_id` dan `claim_token` (atau mencatat galat eksekusi). Server menolak kombinasi kontradiktif (`report`+`campaign` atau `error`+artefak) dengan `400 Bad Request`, menjaga agar penyelesaian ganda/konkuren ditolak (`409 Conflict`), memvalidasi `worker_id`/`claim_token`, menolak penyelesaian bila lease sudah kedaluwarsa (`409 Conflict`, meski belum diklaim worker lain), menolak payload berbeda atau pelaporan `error` susulan terhadap `job_id` yang sudah terkomit (`409 Conflict`), memverifikasi sidik jari dan konsistensi verdict, menduplikasi-tolak percobaan ulang berdasarkan `job_id` di `registry.jsonl` sehingga kegagalan penyimpanan `jobs.json` tidak menggandakan run atau webhook, menandatangani dengan kunci Ed25519 tenant, memersistenkan `jobs.json` secara atomik sebelum mengirim webhook SLA/regresi, serta merekonsiliasi entri `job_id` yang sudah terkomit di `registry.jsonl` ke `jobs.json` saat startup.

```sh
# 1. Antrekan simulasi atau uji kekacauan database langsung
curl -fsSL -X POST http://127.0.0.1:8080/api/v1/jobs \
  -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"tenant":"payments","commit":"a35dc51","kind":"sim","system":"outbox-settlement","seed":7}'

# 2. Jalankan worker jarak jauh di dalam VPC staging privat (otomatis mengirim heartbeat lease; mendukung --id/--worker-id, --tenant, --poll-interval/--poll, --once)
strife worker --server http://127.0.0.1:8080 --token "$STRIFE_PAYMENTS_TOKEN" --id staging-vpc-worker-1 --tenant payments
```

### `POST /api/v1/ingest`

Memasukkan laporan atau kampanye ke dalam registri. Bila `--token` dipasang pada server, permintaan wajib menyertakan `Authorization: Bearer <token>`.

Tubuh permintaan (dibatasi maksimal 32 MiB; ukuran lebih besar mengembalikan `413 Request Entity Too Large`) dapat berupa:

1. **Berkas `report.json` mentah** keluaran `strife sim`, `strife run`, `strife check`, atau `strife shrink`. Metadata opsional `tenant`, `commit`, `branch`, dan `idempotency_key` dapat dilewatkan melalui query string (`/api/v1/ingest?tenant=payments&commit=abc1234&branch=main`), atau lewat header HTTP `Idempotency-Key`.
2. **Berkas `campaign.json` mentah** keluaran `strife campaign`.
3. **Objek amplop (*envelope*)** yang menggabungkan metadata tenant dan git bersama salah satu dari `report` atau `campaign` (tidak boleh keduanya; nilai di dalam amplop mengungguli parameter query):

   ```json
   {
     "tenant": "payments",
     "commit": "714fbd6",
     "branch": "main",
     "idempotency_key": "ci-run-42",
     "report": { ... }
   }
   ```

#### Yang Terjadi Saat Ingest

- **Pemeriksaan integritas sidik jari & verdict:** `strife serve` menghitung ulang `report.Fingerprint` atau `campaign.ComputeFingerprint()` serta memvalidasi konsistensi verdict laporan (`Verdict` vs `Check.Verdict`). Ketidakcocokan atau verdict tidak valid ditolak dengan `400 Bad Request`.
- **Perlindungan replay idempoten (`Idempotency-Key`):** Bila `Idempotency-Key` (header, `?idempotency_key=`, atau field amplop `idempotency_key`) dikirimkan, deduplikasi berlaku per tenant terisolasi melintasi restart server. Mengirim ulang artefak yang sama mengembalikan `200 OK` dengan `"deduplicated": true` serta entri registri semula tanpa menggandakan run atau mengirim ulang webhook; memakai kunci yang sama untuk artefak berbeda ditolak dengan `409 Conflict`.
- **Penandatanganan Ed25519 otomatis (laporan):** Bila `--tenant-keys` cocok dengan tenant yang dinormalisasi (atau `--key` terkonfigurasi), `strife serve` menandatangani laporan yang telah diverifikasi menjadi bundel atestasi `in-toto` v0.1 (`bundle.json`), mencatat `signed: true`, `signer`, dan `key_id` di entri registri, dan mengembalikan `bundle` dalam respons `201 Created`.
- **Pembandingan regresi baseline otomatis (kampanye):** Ketika sebuah kampanye masuk untuk `(tenant, target)`, `strife serve` membandingkannya dengan kampanye tersimpan terbaru milik tenant dan target yang sama memakai `campaign.Compare`, mencatat `regressions`, `fixes`, dan `trace_drifts` pada entri registri dan mengembalikan objek `comparison` penuh pada respons.
- **Peringatan webhook otomatis (`--webhook`):** `strife serve` mengirim notifikasi HTTP POST ke seluruh URL webhook terkonfigurasi apabila:
  - laporan yang masuk melanggar konsistensi (`report.violation`),
  - laporan yang masuk melanggar `--max-mttr` atau `--min-availability` (`report.sla_breach`), atau
  - kampanye yang masuk memunculkan regresi (`campaign.regression`) maupun pelanggaran (`campaign.violation`).
  Deteksi format otomatis mendukung Slack (`hooks.slack.com` atau `?format=slack`), Discord (`discord.com/api/webhooks` atau `?format=discord`), dan endpoint JSON generik.

## Menerbitkan dari CI

`strife sim` (satu `--seed`), `strife campaign`, `strife run`, dan `strife check` seluruhnya mendukung pengiriman langsung hasil ke registri dengan `--push`, `--push-token` (mendukung `env:VAR` atau `file:PATH`), dan `--tenant`:

```sh
strife check --model settlement --history history.jsonl \
  --push http://strife-registry.internal:8080 \
  --push-token "$STRIFE_REGISTRY_TOKEN" \
  --tenant payments

strife campaign --system raft --seeds 1..100 \
  --commit "$(git rev-parse --short HEAD)" --branch "$GITHUB_REF_NAME" \
  --push http://strife-registry.internal:8080 \
  --push-token "$STRIFE_REGISTRY_TOKEN" \
  --tenant payments
```

Untuk penyapuan multi-seed `sim --seeds` atau artefak `report.json` / `campaign.json` yang sudah tersimpan sebelumnya, kirimkan JSON secara langsung ke `/api/v1/ingest`:

```sh
# Kirim laporan satu-seed dengan metadata tenant dan git
curl -fsSL -X POST \
  -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @strife-out/raft-seed7/report.json \
  "http://strife-registry.internal:8080/api/v1/ingest?tenant=payments&commit=$(git rev-parse --short HEAD)&branch=$GITHUB_REF_NAME"

# Kirim kampanye regresi multi-seed
curl -fsSL -X POST \
  -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @strife-campaign/campaign.json \
  "http://strife-registry.internal:8080/api/v1/ingest?tenant=payments"
```

## Dasbor Web Tersemat

Membuka `http://<alamat-dengar>/` di browser menampilkan dasbor mandiri bertema gelap berisi:

- **Kartu ringkasan KPI:** jumlah eksekusi dan kampanye tersimpan, hitungan `linearizable` vs melanggar, regresi seed yang terdeteksi, serta artefak bertanda tangan Ed25519.
- **Tombol filter interaktif:** saring menurut sistem target (`/?target=...`) atau namespace tenant (`/?tenant=...`).
- **Ringkasan SLA Deret Waktu Target & Tenant:** rekap per `(tenant, target)` menampilkan total eksekusi, hitungan lulus/gagal/regresi, rasio kelulusan, rata-rata ketersediaan, P95 MTTR terbaru, dan P95 MTTR puncak.
- **Tabel Eksekusi & Artefak:** tautan per eksekusi ke payload JSON mentah (`/api/v1/runs/<id>`) serta dossier kepatuhan HTML siap cetak (`/api/v1/runs/<id>/dossier?format=html`), lengkap dengan ketersediaan, MTTR P95, delta terhadap baseline, sidik jari SHA-256, dan ID kunci penandatangan.

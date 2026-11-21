# Berkontribusi ke Strife

> **Bahasa:** [English](../../CONTRIBUTING.md) · Bahasa Indonesia

Terima kasih telah berkontribusi pada **Strife**. Untuk menjaga engine tetap cepat, mudah diaudit, dan dapat diputar ulang identik bit demi bit, setiap kontribusi wajib mematuhi empat invarian rekayasa inti yang dijabarkan di sini dan di [`GOVERNANCE.md`](GOVERNANCE.md).

---

## Invarian Rekayasa Inti

1. **Nol Dependensi Eksternal (`stdlib` murni)**
   - [`go.mod`](../../go.mod) harus memiliki **nol** direktif `require`.
   - Protokol jaringan, primitif kriptografi (`crypto/ed25519`, `crypto/sha256`), soket Netlink, kendali cgroup v2, daemon HTTP/dasbor (`strife serve`), dan VM WebAssembly 1.0 diimplementasikan langsung di atas pustaka standar Go.
2. **Determinisme Simulasi Bit Demi Bit**
   - Di dalam `pkg/sim`, `pkg/sim/raft`, `pkg/explore`, `pkg/nemesis`, dan `pkg/workload`, jangan pernah membaca waktu jam dinding (`time.Now()`), `crypto/rand`, maupun status global `math/rand`.
   - Turunkan seluruh bilangan acak dari `internal/rng.RNG` (`xoshiro256**` yang di-seed via `SplitMix64`).
   - Jangan pernah mengiterasi `map` Go pada jalur yang memengaruhi penjadwalan event, urutan pengiriman pesan, atau riwayat operasi; urutkan kuncinya terlebih dahulu.
3. **Kode Bersih & Tanpa *Overengineering***
   - Utamakan `struct` konkret dan fungsi kecil bertunggal-tujuan alih-alih antarmuka spekulatif atau lapisan abstraksi yang berjenjang dalam.
   - Pastikan pemformatan, skrip shell, alur kerja CI, dan analisis statis tetap bersih: `make lint` wajib lulus tanpa peringatan.
4. **Skema Keluaran Kompatibel Mundur**
   - `report.json`, `history.jsonl`, `dossier.json`, `campaign.json`, dan `bundle.json` diatur oleh Skema JSON berversi di [`schemas/`](../../schemas).
   - Penambahan bidang opsional wajib disertai pembaruan snapshot skema melalui `go test ./schemas -update`. Penghapusan, pengubahan nama, atau pengubahan tipe bidang yang sudah ada otomatis ditolak oleh `TestCompatible`.

---

## Verifikasi Lokal

Sebelum membuka *pull request*, jalankan rangkaian verifikasi berikut secara lokal:

```sh
# 1. Pemeriksaan format, go vet, shellcheck, dan actionlint
gofmt -s -w .
make lint

# 2. Unit test cepat dengan detektor balapan data
go test -race -short ./...

# 3. Suite pengujian penuh (mencakup regresi penemuan bug dan pemeriksaan Skema JSON)
go test ./...

# 4. Uji ringkas determinisme & pengecilan skenario
go build -o bin/strife ./cmd/strife
./bin/strife sim --system raft --seeds 1..50 --determinism --expect linearizable --out ""
./bin/strife sim --system raft-ledger --seeds 1..20 --determinism --expect no-anomaly --out ""
./bin/strife sim --system 2pc-ledger --seeds 1..20 --determinism --expect no-anomaly --out ""
./bin/strife sim --system outbox-settlement --seeds 1..20 --determinism --expect no-anomaly --out ""
```

### Pemeriksaan Situs Web & Jembatan WebAssembly

[`site/`](../../site) dibangun langsung dari dokumentasi dwibahasa repositori (`README.md`, `README.id.md`, `CHANGELOG.md`, `docs/id/CHANGELOG.md`, `docs/*.md`, `docs/id/*.md`, `examples/*/README.md`, `docs/id/examples/*/README.md`) dan mengompilasi paket engine inti (`pkg/explore`, `pkg/checker`, `pkg/report`, `pkg/attest`, `pkg/compliance`) menjadi WebAssembly melalui `site/wasm`. Apabila menyentuh berkas-berkas tersebut:

```sh
# Periksa modul jembatan WebAssembly (diwajibkan oleh pages.yml)
(cd site/wasm && test -z "$(gofmt -l .)" && go vet ./... && GOOS=js GOARCH=wasm go vet . && go test ./...)

# Bangun penuh situs statis + pemeriksa tautan + validator redaksi kata (perlu Hugo 0.148.2 + Node 20+)
OFFLINE=1 site/build.sh
```

> [!NOTE]
> - [`site/layouts/_partials/docs-adapter.html`](../../site/layouts/_partials/docs-adapter.html) memecah [`README.md`](../../README.md) dan [`README.id.md`](../../README.id.md) berdasarkan tajuk `## ` tingkat atas. Bila Anda menambah atau mengubah nama tajuk `## ` pada salah satu README, daftarkan slug dan metadatanya di `docs-adapter.html`.
> - Pertahankan tabel `## Wording guidelines` pada [`docs/ROADMAP.md`](../ROADMAP.md); [`site/scripts/check-wording.sh`](../../site/scripts/check-wording.sh) membaca kolom "Avoid" dan menggagalkan CI apabila redaksi terlarang maupun klaim sertifikasi regulator muncul di halaman HTML yang dihasilkan.

### Fuzzing Parser Masukan Tak Tepercaya

Jika Anda menyentuh parser atau pemeriksa ketepatan (*soundness*), jalankan target fuzz terkait secara singkat sebelum membuka PR (CI selalu memutar ulang korpus regresi tersimpan pada setiap job `test`, sementara `ci.yml` menjalankan kesembilan target fuzz selama 45 detik setiap minggu pada `schedule`, lewat `workflow_dispatch`, atau pada PR berlabel `fuzz`):

```sh
go test -run '^$' -fuzz '^FuzzParseWasm$' -fuzztime 15s ./pkg/workload
go test -run '^$' -fuzz '^FuzzReadEDN$' -fuzztime 15s ./pkg/history
go test -run '^$' -fuzz '^FuzzReadJSONL$' -fuzztime 15s ./pkg/history
go test -run '^$' -fuzz '^FuzzCheckAppendSound$' -fuzztime 15s ./pkg/txn
go test -run '^$' -fuzz '^FuzzRESP$' -fuzztime 15s ./pkg/client
go test -run '^$' -fuzz '^FuzzPostgresMessages$' -fuzztime 15s ./pkg/client
go test -run '^$' -fuzz '^FuzzMySQLPackets$' -fuzztime 15s ./pkg/client
go test -run '^$' -fuzz '^FuzzKafkaRecords$' -fuzztime 15s ./pkg/client
```

---

## Lokasi Melakukan Perubahan

- **Model konsistensi (`pkg/model`, `pkg/checker`, `pkg/txn`):**
  Implementasikan `model.Model` (dan opsional `model.Diagnoser`, `model.Decider` berstatus 3-kondisi `DecisionStatus`, `model.CompleteSearcher`, atau `model.ReadOnly` untuk ekstraksi saksi langsung maupun linearisasi bacaan cepat), daftarkan modelnya di [`model.ByName`](../../pkg/model/model.go) serta `model.Names()`, lalu tambahkan unit test + uji silang brute-force di `pkg/model` dan `pkg/txn` beserta uji kesetaraan diferensial pengoptimal WGL di `pkg/checker`.
- **Sistem simulasi atau mode bug (`pkg/sim`, `pkg/sim/raft`, `pkg/explore`):**
  Daftarkan sistem di [`explore.Systems()`](../../pkg/explore/explore.go) beserta `Expect` verdict dan daftar gangguan yang disarankan, tambahkan uji regresi di [`pkg/explore/explore_test.go`](../../pkg/explore/explore_test.go), lalu catat pengukuran empirisnya di [`BENCHMARKS.md`](BENCHMARKS.md) dan [`docs/BENCHMARKS.md`](../BENCHMARKS.md).
- **Klien database langsung (`pkg/client`, `pkg/runner`):**
  Implementasikan `runner.TargetClient` memakai paket `net` / `crypto/tls` pustaka standar, tambahkan target fuzz bagi dekoder protokolnya, klasifikasikan galat pasti (*fail*) vs tak pasti (`info`) secara sah (*sound*), dan sediakan contoh Docker Compose di bawah [`examples/`](../../examples).
- **Transport, gangguan & telemetri (`pkg/transport`, `pkg/agent`, `cmd/strife-agent`, `pkg/probe`):**
  Dukung semantik injeksi/pemulihan idempoten, perbarui pemeriksaan kapabilitas `strife doctor` jika menambah prasyarat host, dan tambahkan aturan korelasi secara ringkas di `pkg/probe`.
- **Laporan, SARIF, kepatuhan & registri run (`pkg/report`, `pkg/compliance`, `pkg/attest`, `pkg/serve`):**
  Pastikan bidang JSON baru di laporan maupun dossier bersifat aditif dan lolos validasi [`schemas/`](../../schemas) (`go test ./schemas`). Jangan pernah mengklaim sertifikasi hukum pada pemetaan kepatuhan.
- **Beban kerja kustom (`pkg/workload`):**
  Pertahankan determinisme WebAssembly 1.0, penghitung instruksi (`DefaultMaxFuel`), batas kedalaman stack panggilan, serta pembatasan memori linier.

---

## Pedoman Pull Request

- Tinjau [`GOVERNANCE.md`](GOVERNANCE.md) sebelum memulai perubahan yang menyentuh skema, kode keluar CLI, determinisme, maupun kebijakan dependensi inti.
- Jaga agar setiap *pull request* terfokus pada satu perubahan yang koheren. Hindari penyuntingan format acak di luar lingkup perubahan Anda.
- Untuk perubahan yang berdampak pada pengguna, tambahkan entri di bawah `## [Unreleased]` pada [`CHANGELOG.md`](../../CHANGELOG.md) dan [`CHANGELOG.md`](CHANGELOG.md).

## Daftar Periksa Pull Request

- [ ] [`go.mod`](../../go.mod) tidak memiliki dependensi eksternal.
- [ ] Keluaran `gofmt -l .` kosong dan `make lint` (`go vet`, `shellcheck`, `actionlint`) lulus.
- [ ] `go test -race -short ./...` dan `go test ./...` lulus (termasuk `./schemas` dan `./pkg/explore`).
- [ ] Fitur baru maupun perbaikan bug disertai unit test deterministik (dan pemeriksaan `site/wasm` / tautan dokumentasi bila relevan).

Dengan mengirimkan kontribusi, Anda menyetujui bahwa karya Anda dilisensikan di bawah [Apache License 2.0](../../LICENSE).

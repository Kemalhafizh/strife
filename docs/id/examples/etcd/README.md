# Contoh: etcd 3-node di bawah Strife

> **Bahasa:** [English](../../../../examples/etcd/README.md) · Bahasa Indonesia

Prasyarat: host Docker Linux (IP kontainer harus dapat dijangkau langsung dari host), Docker Compose v2, dan biner Strife yang dibangun dari akar repositori. CI menjalankan skenario ini di setiap push (`.github/workflows/live.yml`).

```sh
cd examples/etcd
docker compose up -d --build

# Lihat persis perintah yang akan dijalankan tanpa mengubah cluster:
strife plan --config strife.json

# Jalankan uji linearizability key-value (2 menit) dan tulis laporan ke ./strife-out/
strife run --config strife.json

# Jalankan transaksi list-append pada strict serializability (--expect no-anomaly meluluskan CI bila tidak ada anomali)
strife run --config strife-append.json --expect no-anomaly

docker compose down -v
```

`latency` dan `loss` membutuhkan modul kernel `sch_netem` di Docker host (`sudo modprobe sch_netem`). Tanpa modul tersebut, Strife melaporkan kegagalan perintah gangguan dan keluar dengan kode selain nol; lewati kedua gangguan tersebut dengan `strife run --config strife.json --faults partition,kill,pause`.

## Konfigurasi di Direktori `examples/etcd`

| Konfigurasi / Berkas | Model & Transport | Gangguan | Dijalankan di CI (`live.yml`) |
|---|---|---|---|
| `strife.json` | `kv` lewat `docker` | `partition`, `kill`, `pause`, `latency`, `loss` | job `etcd` (linearizable) |
| `strife-append.json` | `append` (`strict-serializable`) lewat `docker` | `partition`, `kill`, `pause`, `latency`, `loss` | job `etcd` (`--expect no-anomaly`) |
| `strife-agent.json` + `docker-compose.agent.yml` | `kv` lewat `agent` (`strife-agent` port 9292) | `partition`, `kill`, `pause` | job `agent` + `strife bench inject` |
| `strife-cgroup.json` | `kv` lewat `docker` (`cgroup: auto`, `io_device: auto`, `sudo: true`) | `freeze`, `io-stall` | job `cgroup` |

## Menggunakan `strife-agent` (`docker-compose.agent.yml`)

Untuk menjalankan daemon berlatensi rendah di dalam tiap kontainer etcd alih-alih memakai `docker exec`:

```sh
go build -o bin/strife ./cmd/strife
CGO_ENABLED=0 go build -o bin/strife-agent ./cmd/strife-agent
export STRIFE_AGENT_TOKEN=$(openssl rand -hex 16)

cd examples/etcd
docker compose -f docker-compose.yml -f docker-compose.agent.yml up -d --build
../../bin/strife run --config strife-agent.json
docker compose -f docker-compose.yml -f docker-compose.agent.yml down -v
```

## Menambahkan Gangguan cgroup (`freeze`, `io-stall`)

Gangguan ini bekerja pada cgroup v2 milik masing-masing kontainer dari Docker host, sehingga Strife harus berjalan sebagai root (atau memakai `"sudo": true` pada `transport`). `strife-cgroup.json` menggunakan `"cgroup": "auto"` dan `"io_device": "auto"` agar ditemukan otomatis saat berjalan. Untuk memeriksa dan menentukannya secara manual:

```sh
for c in strife-etcd1 strife-etcd2 strife-etcd3; do
  echo "$c /sys/fs/cgroup/system.slice/docker-$(docker inspect -f '{{.Id}}' $c).scope"
done
lsblk -no MAJ:MIN "$(df --output=source /var/lib/docker | tail -1)"
```

```json
{ "name": "n1", "ip": "172.30.0.11", "container": "strife-etcd1",
  "cgroup": "/sys/fs/cgroup/system.slice/docker-<id>.scope" }
```

dan pada bagian `nemesis`: `"faults": ["freeze", "io-stall"], "io_device": "8:0"`.

`clock-skew` ditolak pada transport `docker` karena kontainer berbagi jam kernel host; gunakan transport `ssh` atau `agent` pada VM yang terisolasi untuk pengujian tersebut.

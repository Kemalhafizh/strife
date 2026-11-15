# Contoh: etcd di Kubernetes di bawah Strife

> **Bahasa:** [English](../../../../examples/kubernetes/README.md) · Bahasa Indonesia

Cluster etcd tiga anggota yang sama seperti pada [`examples/etcd`](../etcd/README.md), dideploy sebagai StatefulSet dan dikendalikan melalui `kubectl exec`. Strife hanya memerlukan `kubeconfig`: tanpa SSH, tanpa agen, dan tanpa akses langsung ke node host. CI menjalankan contoh ini di dalam cluster [kind](https://kind.sigs.k8s.io) pada setiap push (job `kubernetes` di `.github/workflows/live.yml`).

```sh
# Siapkan cluster kind, image etcd, StatefulSet, dan rute dari mesin ini ke
# jaringan pod (sehingga klien Strife menjangkau pod langsung lewat IP-nya):
scripts/live/kind-etcd.sh

strife plan --config examples/kubernetes/strife.json
strife run  --config examples/kubernetes/strife.json

scripts/live/kind-etcd.sh delete
```

## Cara Konfigurasi Dipetakan ke Cluster

```json
"nodes": [{ "name": "n1", "ip": "auto", "pod": "etcd-0", "container": "etcd" }, ...],
"transport": { "type": "kubernetes", "namespace": "strife" },
"db": { "kind": "etcd", "endpoints": ["http://{ip}:2379"], ... }
```

- `pod` adalah nama pod di `transport.namespace`, atau dalam format `namespace/name`. `container` memilih kontainer di dalam pod.
- `"ip": "auto"` membaca IP masing-masing pod (`.status.podIP`) ketika eksekusi dimulai; aturan partisi menjatuhkan lalu lintas dari IP tersebut. Pod StatefulSet mempertahankan IP-nya ketika kontainer dimulai ulang oleh gangguan `kill`, tetapi tidak ketika pod dijadwalkan ulang—karena itu IP selalu dibaca ulang setiap kali eksekusi dimulai.
- Endpoint yang memuat `{ip}` diperluas menjadi satu endpoint per node.
- `transport` juga menerima `kubectl` (jalur biner), `context`, dan `kubeconfig`.

## Kebutuhan pada Pod Target

- Kapabilitas `NET_ADMIN` untuk `partition` (iptables), serta `latency` dan `loss` (`tc netem`, yang juga memerlukan modul kernel `sch_netem` termuat pada node).
- Perkakas `iptables`, `iproute2`, dan `procps` tersedia di dalam image kontainer.
- PID 1 bukan proses database: di sini PID 1 berupa `sleep`, sehingga `kill` dapat mengirim SIGKILL ke proses etcd dan nemesis menyalakannya kembali dengan `db.start`. Bila database menjadi PID 1, Kubernetes me-restart seluruh kontainer dan nemesis kehilangan kendali siklusnya.

## Batasan

- `freeze` dan `io-stall` bekerja pada cgroup milik node host sehingga ditolak: transport `kubernetes` tidak memiliki akses ke host node. Jalankan `strife-agent` di node tersebut dan gunakan transport `agent` untuk kedua gangguan ini.
- `clock-skew` ditolak karena pod berbagi jam kernel node yang sama.
- Strife harus dapat menjangkau IP pod. Dari dalam cluster hal ini berjalan langsung; dari luar cluster, rutekan CIDR pod (seperti dilakukan `kind-etcd.sh`) atau jalankan Strife di dalam sebuah pod dengan ServiceAccount yang memiliki izin `get pods` dan `create pods/exec`.

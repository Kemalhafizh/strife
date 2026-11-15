# strife-agent: daemon gangguan per node

> **Bahasa:** [English](../../../../examples/agent/README.md) · Bahasa Indonesia

`strife-agent` adalah daemon kecil dan ringan yang berjalan langsung di dalam node target (kontainer, VM, atau mesin bare-metal) untuk menerapkan gangguan dengan *syscall* langsung alih-alih meluncurkan shell di setiap perintah.

---

## Mengapa `strife-agent`?

Dalam pengujian sistem terdistribusi, presisi waktu sangat menentukan. Gangguan yang dijadwalkan pada `t = 1.050s` harus menghantam kernel sedekat mungkin dengan batas milidetik tersebut.

Perkakas konvensional menyuntikkan gangguan dengan mengeksekusi proses shell (`docker exec container iptables ...` atau `ssh user@node tc ...`): setiap transisi membayar biaya pembuatan proses baru, perjalanan bolak-balik Docker atau SSH, serta inisialisasi shell. Diukur dengan `strife bench inject` pada runner GitHub terhadap cluster etcd 3-node (50 transisi per baris; tabel, metode, dan catatan lengkap di [`docs/id/BENCHMARKS.md`](../../BENCHMARKS.md#1-latensi-injeksi-gangguan)):

| Gangguan | `docker exec` P50 / P95 | `strife-agent` P50 / P95 | Di dalam agen (P50) |
|---|---|---|---|
| `pause` | 43,7 / 45,1 ms | **1,36 / 1,90 ms** | 372 µs |
| `kill` | 44,4 / 46,1 ms | **1,58 / 1,99 ms** | 495 µs |
| `partition` | 57,9 / 61,3 ms | **4,02 / 4,29 ms** | 2,61 ms |
| `latency` | 42,8 / 44,1 ms | 48,1 / 59,5 ms ¹ | **159 µs** |

¹ Balasan agen keluar melewati antarmuka yang baru saja diperlambat, sehingga konfirmasinya tiba terlambat; qdisc netem sebenarnya telah terpasang 159 µs setelah permintaan masuk. Partisi pada run pengukuran masih menjalankan biner `iptables` di dalam agen (kini tersedia *AF_NETLINK* FIB blackhole dengan fallback otomatis ke `iptables`); sinyal, netem, dan penulisan cgroup berupa *syscall* langsung.

Sinyal dan perubahan netem mendarat ~30× lebih cepat daripada melalui `docker exec`, yang krusial saat gangguan harus mengejar jendela sempit seperti kedaluwarsa *leader lease* atau pergantian leader (*failover*).

---

## Arsitektur & Mekanisme

```
+-------------------------------------------------------------+
| Strife Runner                                               |
|  (pkg/transport/agent.go + FaultApplier)                    |
+------------------------------+------------------------------+
                               | Persistent HTTP Keep-Alive
                               v
+-------------------------------------------------------------+
| strife-agent daemon (cmd/strife-agent, port 9292)           |
|   /health  /apply  /exec  /teardown  /probes                |
+-------------------------------------------------------------+
   |                  |                     |               |
   v                  v                     v               v
RTNETLINK       Direct Signals         cgroup v2        iptables fallback
(QDISC + FIB)   (syscall.SIGSTOP)     (/sys/fs/cgroup)  (STRIFE chain)
  Netem + FIB     Process Freeze        cgroup.freeze     Network Partitions
  Blackhole       SIGKILL Crash         io.max Stall
```

1. **RTNETLINK (`RTM_NEWQDISC` + `RTM_NEWRULE` / `FR_ACT_BLACKHOLE`):**
   Berkomunikasi langsung dengan subsistem jaringan kernel Linux melalui soket `AF_NETLINK` untuk mengonfigurasi qdisc `netem` (`latency`, `jitter`, `loss`) dan aturan routing blackhole FIB kernel bagi partisi jaringan tanpa *fork*.
2. **Sinyal Syscall Langsung:**
   Mengganti `pkill -STOP` dan `pkill -9` dengan pemindaian `/proc` langsung serta `syscall.Kill(pid, sig)` (termasuk penemuan target Kubernetes `pod:<ns>/<name>`, `k8s:<name>`, dan `container:<id>`).
3. **Penulisan Berkas cgroup v2 Langsung & Telemetri (`/probes`):**
   Menulis langsung ke `/sys/fs/cgroup/.../cgroup.freeze` dan `/sys/fs/cgroup/.../io.max`, serta menyajikan telemetri PSI, OOM, kenari `fsync`, dan lompatan jam pada `GET /probes`.
4. **Fallback `iptables` Idempoten:**
   Ketika *policy routing* FIB kernel tidak tersedia, agen otomatis beralih ke rantai `iptables` `STRIFE` khusus dan membersihkan aturan secara tuntas saat pemulihan atau *teardown*.

---

## Mulai Cepat

### 1. Jalankan Cluster Agen

```sh
cd examples/agent
export STRIFE_AGENT_TOKEN=$(openssl rand -hex 16)   # wajib dibawa setiap permintaan
docker compose up -d --build
```

Agen menolak mendengarkan pada alamat non-loopback tanpa token (`-insecure-no-token` dapat menonaktifkannya khusus untuk lingkungan uji sementara), dan membandingkan token dalam waktu konstan.

### 2. Verifikasi Kesehatan & Kapabilitas Agen

```sh
curl -s -H "Authorization: Bearer $STRIFE_AGENT_TOKEN" http://localhost:9291/health | jq
```

Keluaran:
```json
{
  "node": "node1",
  "uptime_seconds": 12,
  "active_faults": 0,
  "capabilities": {
    "os": "linux",
    "arch": "amd64",
    "netlink": true,
    "netlink_fib_rule": true,
    "cgroup_v2": true,
    "kubernetes": false,
    "direct_signals": true,
    "nftables": false,
    "iptables": true,
    "tc": true
  }
}
```

### 3. Jalankan Uji Kekacauan dengan Transport Agen

```sh
strife run --config strife.json
```

Selama eksekusi, Strife mencatat untuk setiap transisi seberapa lama seluruh node terkait mengonfirmasi gangguan tersebut setelah jadwal rencananya, diikuti ringkasan; `report.json` menyimpan bidang `landed` dan `latency` yang sama untuk setiap transisi:
```
   8.91s inject pause [1] (landed 1.4ms after plan)
  17.51s heal pause [1] (landed 1.5ms after plan)
...
fault timing: 14 transitions landed p50=1.6ms p95=4.3ms max=7.9ms after plan
```

Untuk mengukur sendiri latensi di cluster Anda:
```sh
strife bench inject --config strife.json --transports docker,agent --faults partition,pause,kill --cycles 50
```

### 4. Pembersihan

```sh
docker compose down -v
```

# Contoh: Valkey primary + replika di bawah Strife

> **Bahasa:** [English](../../../../examples/valkey/README.md) · Bahasa Indonesia

Satu primary Valkey (`n1`) dengan dua replika asinkron (`n2`, `n3`). Setiap klien Strife terikat ke satu node, menyerupai aplikasi yang connection pool-nya menyebar operasi baca ke replika:

- klien pada primary melakukan operasi baca dan tulis di sana;
- klien pada replika hanya membaca darinya, sedangkan permintaan tulisnya ditolak dengan `READONLY` (dicatat sebagai kegagalan pasti, bukan anomali).

```sh
cd examples/valkey
docker compose up -d --build
```

## 1. Pembacaan dari Replika Asinkron Tidak Linearizable

```sh
strife run --config strife.json --expect violation
```

Skenario ini diharapkan **melanggar linearizability**. Ketika replika terputus dari primary, replika terus menjawab operasi baca memakai data terakhir yang sempat direplikasi sementara primary terus maju memproses penulisan baru. Strife melaporkan pembacaan basi tersebut lengkap dengan penulisan yang terlewatkannya (ini adalah perilaku terdokumentasi Valkey/Redis, bukan bug implementasi).

## 2. Primary Tunggal Bertahan Melewati `kill -9`

```sh
strife run --config strife-primary.json --expect linearizable
```

Seluruh klien memakai primary yang berulang kali dihentikan paksa dengan SIGKILL dan dinyalakan lagi. Dengan `appendonly yes`, server menambahkan tiap penulisan ke AOF lewat `write()` sebelum membalas klien, sehingga crash proses tidak menghilangkan penulisan yang sudah diakui dan riwayat tetap linearizable. Kehilangan mesin secara fisik adalah kasus berbeda: dengan `appendfsync everysec` hingga satu detik penulisan yang sudah diakui bisa saja masih berada di *page cache* OS. Coba gunakan `STRIFE_FSYNC=always` pada `docker-compose.yml` untuk pengaturan yang ketat.

## Yang Diuji oleh CI

`.github/workflows/live.yml` menjalankan kedua skenario di setiap push melalui GitHub Action milik Strife sendiri. Skenario 1 wajib menemukan pelanggaran dan skenario 2 wajib lulus.

```sh
docker compose down -v
```

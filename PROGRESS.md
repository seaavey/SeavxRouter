# Progres dashboard SeavxRouter

PR: [#1 — Add Vite web dashboard](https://github.com/seaavey/SeavxRouter/pull/1)  
Branch: `feat/web-dashboard` → `main`

## Sudah masuk PR

- [x] Dashboard Vite, React, TypeScript, Tailwind CSS 4, komponen berbasis Radix dengan pola shadcn/ui, dan Motion.
- [x] Overview, Requests, Providers, Models, API Keys, Sign in, dan setup kata sandi pertama. Mode demo tersedia di `/?demo=1`.
- [x] SSE terautentikasi untuk memperbarui Requests dan Overview. Pembaruan digabung dalam interval dua detik.
- [x] Login admin dengan SQLite dan bcrypt. Setup pertama via localhost mewajibkan penggantian kata sandi awal. Cookie sesi juga melindungi SSE dan API dashboard.
- [x] Build Vite dan pengujian Go untuk autentikasi. Smoke test proxy Vite ke Go mencakup login, setup awal, aliran SSE, dan pencabutan akses setelah logout.
- [x] Review demo di Chromium pada ukuran desktop, termasuk pemilih periode, pencarian Requests, dan dialog detail.

## Pekerjaan tersisa untuk PR #1

Sebagian perubahan backend dan embedding sempat dikerjakan secara lokal sebelum workspace terputus. Periksa dan pulihkan diff lokal jika tersedia. Checklist di bawah baru ditandai selesai setelah perubahan tersedia di branch dan pengujiannya lulus.

- [ ] Tampilkan semua provider yang dikonfigurasi, termasuk provider yang tidak melaporkan kuota. Beri keterangan ketika detail kuota tidak tersedia.
- [ ] Gunakan cursor stabil untuk Requests. Tambahkan pencarian server-side dan kolom akun serta tenant. Pastikan log baru tidak mengulang atau melewatkan baris saat pindah halaman.
- [ ] Tambahkan edit budget USD dan token limit melalui handler PATCH yang sudah ada. Tampilkan progres penggunaan sejak awal bulan UTC dengan sumber data yang sama dengan enforcement Go.
- [ ] Hubungkan **TanStack Query** ke API dashboard. Gunakan invalidasi query terjadwal saat SSE menerima data dan bersihkan cache privat saat sesi berakhir.
- [ ] Pakai **TanStack Table** untuk tabel Requests, Models, dan API Keys. Requests tetap memakai filter dan cursor dari backend.
- [ ] Pakai **TanStack Router** untuk URL per halaman, deep link, refresh, serta Back/Forward. Pastikan fallback route berjalan pada Vite dan binary produksi.
- [ ] Pakai **TanStack Form + Zod** untuk login, setup kata sandi, create API key, dan edit limit. Selaraskan batas input dengan validasi Go, termasuk konfirmasi kata sandi, batas panjang, dan angka nonnegatif.
- [ ] Embed hasil build Vite ke binary Go. Verifikasi rilis dapat melayani dashboard dan API dari origin yang sama tanpa proses Node terpisah.
- [ ] Tambahkan CI untuk instalasi bersih, typecheck, build frontend, build Go dengan aset web, dan Playwright E2E.
- [ ] Terapkan komposisi warna sekitar 60% grafit gelap, 30% abu gelap, dan 10% cyan. Pakai komponen shadcn/ui dan pertahankan animasi singkat yang menghormati reduced motion.

## Pengujian sebelum merge

| Viewport | Fokus pengujian |
| --- | --- |
| HP · 360 × 800 dan 390 × 844 | Navigasi, kartu metrik, scroll tabel, dialog, dan form saat keyboard terbuka |
| Tablet · 768 × 1024 dan 1024 × 768 | Grid provider serta tabel Requests dan API Keys dalam orientasi tegak dan mendatar |
| Laptop · 1366 × 768 | Hierarki visual, detail request, dan form edit limit |
| Desktop · 1920 × 1080 | Lebar konten, keterbacaan grafik, dan jarak antarpanel |

- [ ] Ambil screenshot Sign in, setup password, Overview, Requests, Providers, Models, dan API Keys. Catat viewport dan commit untuk setiap hasil uji.
- [ ] Jalankan Playwright dengan backend Go dan database SQLite temporer. Uji setup pertama, persistensi password setelah restart, login ulang, dan logout.
- [ ] Uji request masuk lewat SSE, reconnect, pencarian dan pindah halaman saat log baru masuk, serta kondisi data kosong.
- [ ] Uji create, edit, dan revoke API key. Cocokkan progres pemakaian dengan perhitungan backend dan pastikan input tidak valid ditolak.
- [ ] Jalankan Go test dan build Vite, lalu buka dashboard langsung dari binary untuk memverifikasi aset, deep link, dan SSE.

## Status terakhir

Versi demo desktop sudah pernah dibuka di Chromium. Screenshot per halaman dan uji responsif lintas viewport masih tertunda. Implementasi TanStack, Form/Zod, embedding, dan build akhir belum terverifikasi di PR.

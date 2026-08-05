import type { UserRow } from '@/lib/api';
import type { DataTableColumn } from '@/lib/dataTable';

// Users tablosunun kolon bütçesi (v0.9.660).
//
// Operatör-bildirimi: "Users tablosunda tablolar kaymış biraz düzelt."
// Ekran görüntüsünde tablo sağdan taşıyor, Actions kolonu ("Reset
// password" / "Delete") kesiliyor ve sayfanın kendisi yatay kayıyor.
//
// KÖK NEDEN — iki kusur üst üste bindi:
//
//  1. İKİ `flex` kolon vardı (email + team). Primitifin kendi belgesi
//     TEK emici öngörüyor ("Emici kolon içerik olarak EN ÇOK yer
//     isteyen olmalı"). `table-layout: fixed` artan genişliği auto
//     kolonlara EŞİT dağıtır — kısa bir metin alanı olan Team, email
//     kadar pay alıyordu.
//  2. `flex` → `width: auto` demek ve fixed layout'ta auto bir kolon,
//     diğerlerinin bildirilen toplamı tabloyu doldurduğunda **0'a
//     çöker**. Sabit bütçe 1030px'ti; operatörün ~1240px'lik içerik
//     alanında email'e bir kez kalıcı genişlik yazıldığı anda Team
//     kolonu tamamen görünmez oldu — düzenlenebilir bir alan, sıfır
//     genişlikte. Ekran görüntüsünde TEAM başlığı hiç yok.
//
// Bu dosya ayrı duruyor çünkü bütçe VERİDİR ve test edilebilir olması
// gerekiyor: usersColumns.test.ts toplamı ölçüyor. Kolon genişlikleri
// sayfanın içinde gömülüyken hiçbir kapı onları kontrol edemiyordu ve
// tam da bu yüzden sessizce ekranı aştılar.
//
// BÜTÇE: hedef içerik genişliği 1240px (1920 ekran @125% ölçek eksi
// kenar çubuğu ve dolgu — operatörün ekran görüntüsünden ölçüldü).
// Sabit kolonlar 1020px, email'e en az 220px kalıyor.

// USERS_CONTENT_BUDGET — tasarım hedefi. Test bunun üzerinden ölçüyor.
export const USERS_CONTENT_BUDGET = 1240;

// USERS_EMAIL_MIN — email hücresinin gerçek asgarisi: 20px avatar +
// 8px boşluk + "yusufcan.baspinar@akbank.com" uzunluğunda bir adres
// (12px/600 ≈ 185px) + hücre dolgusu. Bunun altında adres kırpılır.
export const USERS_EMAIL_MIN = 220;

export const USER_COLS: DataTableColumn<UserRow>[] = [
  // TEK esneyen kolon. Artan genişliğin tamamı buraya gider; içerik
  // olarak en çok yer isteyen kolon bu (adres + altında ad · birim).
  { id: 'email',      label: 'Email',       sortValue: u => u.email,             naturalDir: 'asc', flex: true, minWidth: USERS_EMAIL_MIN },
  // 115 = select minWidth 90 + hücre dolgusu 24.
  { id: 'role',       label: 'Role',        sortValue: u => u.role,              naturalDir: 'asc',  width: 115 },
  // 155 = select minWidth 130 + 24.
  { id: 'customRole', label: 'Custom role', width: 155 },
  // 145 = input minWidth 120 + 24. ARTIK SABİT: flex'ken 0'a çöküyor
  // ve kolon tamamen kayboluyordu.
  { id: 'team',       label: 'Team',        sortValue: u => u.team ?? '',        naturalDir: 'asc', width: 145 },
  // 80 = "LDAP" rozeti (10px büyük harf ≈ 45) + rozet dolgusu + 24.
  // Eskiden 110'du; içerik hiçbir zaman o kadar yer istemedi.
  { id: 'provider',   label: 'Provider',    sortValue: u => u.authProvider ?? '', naturalDir: 'asc', width: 80 },
  // v0.8.403 — presence. Sort key puts online users first, then most
  // recently seen; never-seen (no stamp) sinks to the bottom.
  // 95 = "● online" rozeti ≈ 62 + 24.
  { id: 'seen',       label: 'Last seen',   sortValue: u => u.lastSeenAt ?? 0,   naturalDir: 'desc', width: 95 },
  // v0.8.450 — kalıcı son LOGIN anı (operatör isteği). "Last seen"
  // Redis-TTL'li aktivite damgası; bu kolon users tablosundan gelir
  // ve hiç sönmez. 90 = "20h ago" mono ≈ 55 + 24.
  { id: 'lastLogin',  label: 'Last login',  sortValue: u => u.lastLoginAt ?? 0,  naturalDir: 'desc', width: 90 },
  // 145: hücre artık SANİYESİZ yazıyor ("05.08.2026 10:09"), tam damga
  // title'da. Bir hesabın oluşturulma saniyesi kolon genişliğine
  // değmiyor; 170px'ti ve tablodaki en pahalı ikinci kolondu.
  { id: 'created',    label: 'Created',     sortValue: u => u.createdAt,         naturalDir: 'desc', width: 145 },
  // 195 = "Reset password" (.sec ≈ 105) + 6 boşluk + "Delete" (≈ 52)
  // + 24. Eskiden 230'du; fazlalık doğrudan taşmaya gidiyordu.
  { id: 'actions',    label: 'Actions',     align: 'right', width: 195 },
];

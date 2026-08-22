// chatUrl — CoSRE sohbet çekmecesinin URL bağı (v0.9.1258).
//
// Denetim bulgusu: konuşma sunucuda KALICI (v0.9.1139) ama açık thread
// URL'de yaşamıyordu — sayfa yenilenince çekmece ve konuşma gidiyordu
// (URL-kaynak-doğruluk sapması; v0.8.256 drawer-param sınıfı).
// Sözleşme: ?chat=<convId> ⇔ çekmece açık + o konuşma yüklü.
// Varsayılan (kapalı/kimliksiz) URL'e YAZILMAZ — eski linkler bayt-bayt.

// syncChatParam — state → URL yarısının SAF karar çekirdeği. Değişiklik
// gerekmiyorsa null döner (gereksiz replace-write, history/searchParams
// kimliğini çalkalamasın); gerekiyorsa YENİ URLSearchParams (prev kopyası
// — yabancı paramlar korunur).
export function syncChatParam(
  prev: URLSearchParams, conversationId: string | null, open: boolean,
): URLSearchParams | null {
  const cur = prev.get('chat');
  const want = open && conversationId ? conversationId : null;
  if (cur === want || (!cur && !want)) return null;
  const next = new URLSearchParams(prev);
  if (want) next.set('chat', want); else next.delete('chat');
  return next;
}

// Exception sample-scan envelope → the sentence an operator reads when the
// sample list comes back empty.
//
// v0.9.463 split "no samples" from "the candidate budget ran out". v0.9.795
// (operator-reported) makes the scan paged and splits the second half again:
// hitting the 5000-candidate ceiling and reading the group's window to the
// end are DIFFERENT answers. Before this, a finished group whose siblings
// (same service + exception type) filled the single 500-row page read
// "En yeni 500 aday tarandı" forever — technically true, practically a
// dead end, and the stack-trace box went blank with it.

export type SampleScanEnvelope = {
  scanned?: number;
  scanCapped?: boolean;
  windowExhausted?: boolean;
} | null | undefined;

export type EmptySamplesNote = { text: string; warn: boolean };

// emptySamplesNote — `fallback` is the plain "there is nothing here" line of
// the calling surface, used when the envelope says the scan ended for no
// interesting reason (or an old server sent no envelope at all).
export function emptySamplesNote(env: SampleScanEnvelope, fallback: string): EmptySamplesNote {
  const n = (env?.scanned ?? 0).toLocaleString();
  if (env?.scanCapped) {
    // Warn: this one IS a scan limit, and the operator can act on it
    // (narrow the group, look at the occurrences chart instead).
    return {
      warn: true,
      text: `⚠ En yeni ${n} aday tarandı (tavan), bu grubun örneği çıkmadı — grup ` +
        `aynı servis+tip altındaki kardeş gruplara göre çok seyrek ateşliyor; ` +
        `occurrences grafiği gerçek dağılımı gösterir.`,
    };
  }
  if (env?.windowExhausted) {
    // Not a warning: we really did read everything the group could be in.
    return {
      warn: false,
      text: `Grubun zaman aralığı baştan sona tarandı (${n} aday) — örnek span'ler ` +
        `retansiyon dışı kalmış olabilir; occurrences sayısı grup kaydından gelir.`,
    };
  }
  return { warn: false, text: fallback };
}

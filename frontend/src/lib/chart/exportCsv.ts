// exportCsv — panelin GÖRÜNEN verisinden CSV (FAZ 2D, panel menüsü).
//
// Girdi AlignedData'nın kendisi: ekranda ne varsa o iner — ayrı bir
// export sorgusu YOK (spec'in legend ilkesiyle aynı: zoom pencereleme,
// fetch değil). null hücre BOŞ kalır; "0" yazmak eksik ölçümü veri
// yapardı (dataFrame/gapPolicy sözleşmesinin CSV hâli).
//
// RFC 4180 kaçışı: virgül/tırnak/satır sonu içeren alan çift tırnağa
// alınır, içteki tırnak ikilenir. Seri adları operatör verisinden
// (servis adı, attr değeri) geldiği için kaçış pazarlık değil.

export function csvEscape(field: string): string {
  return /[",\n\r]/.test(field) ? '"' + field.replace(/"/g, '""') + '"' : field;
}

export function alignedToCsv(
  names: ReadonlyArray<string>,
  data: ReadonlyArray<ReadonlyArray<number | null>>,
): string {
  const header = ['time', ...names.map(csvEscape)].join(',');
  const times = data[0] ?? [];
  const rows: string[] = [header];
  for (let r = 0; r < times.length; r++) {
    const cells: string[] = [new Date(times[r] as number).toISOString()];
    for (let c = 1; c < data.length; c++) {
      const v = data[c][r];
      cells.push(v == null ? '' : String(v));
    }
    rows.push(cells.join(','));
  }
  return rows.join('\n') + '\n';
}

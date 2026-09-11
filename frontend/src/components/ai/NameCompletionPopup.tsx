// NameCompletionPopup — v0.10.687 (D4): girişin üstünde servis adı listesi.
// Combobox'ın .cb-list / .cb-row sınıfları (tek liste dili); mousedown ile
// seçim (textarea odağı kaybolmasın). Klavye: CopilotChat onKeyDown.
export function NameCompletionPopup({ id, items, highlight, onPick, onHover }: {
  id: string;
  items: string[];
  highlight: number;
  onPick: (name: string) => void;
  onHover: (i: number) => void;
}) {
  return (
    <div className="cb-list chat-complete" role="listbox" id={id} aria-label="Servis adı önerileri">
      {items.map((n, i) => (
        <div key={n} id={`${id}-${i}`} role="option" aria-selected={i === highlight}
          className={`cb-row${i === highlight ? ' cb-row-on' : ''}`}
          onMouseDown={e => { e.preventDefault(); onPick(n); }}
          onMouseEnter={() => onHover(i)}>
          {n}
        </div>
      ))}
      <div className="cb-row cb-row-empty cb-foot">↑↓ seç · Enter/Tab ekle · Esc kapat</div>
    </div>
  );
}

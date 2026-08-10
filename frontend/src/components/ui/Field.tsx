import { useId, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes, type TextareaHTMLAttributes } from 'react';

// mK10 (v0.9.921) — `aria-describedby` üç varyantta da AYNI kalıpta.
// `Field` bunu v0.9.8xx'ten beri yapıyordu, `SelectField` ve
// `TextareaField` yapmıyordu: ipucu/hata metni EKRANDA duruyor ama
// ekran okuyucu alana odaklandığında OKUNMUYOR. Yani "Bu alan şu
// biçimde olmalı" uyarısı, tam da ona en çok ihtiyaç duyan kullanıcıya
// ulaşmıyordu. Görsel çıktı değişmiyor; eklenen tek şey bağ.
//
// Field — labelled input with optional hint or error. Replaces
// the `<label>{x}</label><input ... />` pairs sprinkled across
// Settings, Login, ChangePassword, and form-driven admin pages.
// Auto-wires htmlFor/id via useId() so click-on-label focuses
// the right input even when the form has 30+ rows.

interface BaseProps {
  label: ReactNode;
  hint?: ReactNode;
  error?: ReactNode;
}

export interface FieldProps extends BaseProps,
  Omit<InputHTMLAttributes<HTMLInputElement>, 'children'> {}

export function Field({ label, hint, error, id, ...input }: FieldProps) {
  const auto = useId();
  const fieldId = id ?? auto;
  return (
    <div className="field">
      <label htmlFor={fieldId} className="field-label">{label}</label>
      <input id={fieldId} aria-invalid={error ? 'true' : undefined}
             aria-describedby={hint || error ? `${fieldId}-hint` : undefined}
             {...input} />
      {error && <span id={`${fieldId}-hint`} className="field-error">{error}</span>}
      {!error && hint && <span id={`${fieldId}-hint`} className="field-hint">{hint}</span>}
    </div>
  );
}

// Select variant — same labelling pattern but for a <select>.
// Children are <option>s, owned by the caller so we don't lose
// the optgroup / non-string label flexibility.
export interface SelectFieldProps extends BaseProps,
  SelectHTMLAttributes<HTMLSelectElement> {}

export function SelectField({ label, hint, error, id, children, ...select }: SelectFieldProps) {
  const auto = useId();
  const fieldId = id ?? auto;
  return (
    <div className="field">
      <label htmlFor={fieldId} className="field-label">{label}</label>
      <select id={fieldId} aria-invalid={error ? 'true' : undefined}
              aria-describedby={hint || error ? `${fieldId}-hint` : undefined}
              {...select}>{children}</select>
      {error && <span id={`${fieldId}-hint`} className="field-error">{error}</span>}
      {!error && hint && <span id={`${fieldId}-hint`} className="field-hint">{hint}</span>}
    </div>
  );
}

// Textarea variant — multi-line content. Rows defaults to 4,
// matching the heaviest current usage (postmortem editor in
// /incident).
export interface TextareaFieldProps extends BaseProps,
  TextareaHTMLAttributes<HTMLTextAreaElement> {}

export function TextareaField({ label, hint, error, id, rows = 4, ...ta }: TextareaFieldProps) {
  const auto = useId();
  const fieldId = id ?? auto;
  return (
    <div className="field">
      <label htmlFor={fieldId} className="field-label">{label}</label>
      <textarea id={fieldId} rows={rows} aria-invalid={error ? 'true' : undefined}
                aria-describedby={hint || error ? `${fieldId}-hint` : undefined}
                {...ta} />
      {error && <span id={`${fieldId}-hint`} className="field-error">{error}</span>}
      {!error && hint && <span id={`${fieldId}-hint`} className="field-hint">{hint}</span>}
    </div>
  );
}

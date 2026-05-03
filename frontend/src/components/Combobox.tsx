'use client';
import { useId } from 'react';

/**
 * Free-text input backed by a native <datalist> dropdown.
 * Users can type to filter, click the field and pick from the list,
 * or press the ✕ button to clear the selection.
 */
export function Combobox({
  value, onChange, options, placeholder, width, onEnter,
}: {
  value: string;
  onChange: (v: string) => void;
  options: string[];
  placeholder?: string;
  width?: number | string;
  onEnter?: () => void;
}) {
  const listId = useId();
  return (
    <div className="cb-wrap" style={{ width }}>
      <input
        list={listId}
        value={value}
        placeholder={placeholder}
        onChange={e => onChange(e.target.value)}
        onKeyDown={e => e.key === 'Enter' && onEnter?.()}
        autoComplete="off"
        spellCheck={false}
      />
      {value && (
        <button className="cb-clear" type="button"
          aria-label="Clear"
          title="Clear"
          onClick={() => onChange('')}
          onMouseDown={e => e.preventDefault() /* keep input focus */}>
          ✕
        </button>
      )}
      <datalist id={listId}>
        {options.map(o => <option key={o} value={o} />)}
      </datalist>
    </div>
  );
}

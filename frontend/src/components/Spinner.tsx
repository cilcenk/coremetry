export function Spinner() { return <div className="spinner" />; }

export function Empty({ icon, title, children }: {
  icon: string; title: string; children?: React.ReactNode;
}) {
  return (
    <div className="empty">
      <div className="icon">{icon}</div>
      <h3>{title}</h3>
      {children && <p>{children}</p>}
    </div>
  );
}

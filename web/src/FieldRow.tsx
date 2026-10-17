// A label and its control, as one row of a .form-grid.

export function FieldRow({ label, help, children }: { label: string; help?: string; children: React.ReactNode }) {
  return (
    <>
      <label title={help}>{label}</label>
      {children}
    </>
  )
}

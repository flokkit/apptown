import type { File as ChangedFile, PackageMove } from '../../wire.gen'

export function changedFiles(packages: PackageMove[], files: ChangedFile[] = []): string {
  if (packages.length) return packages.map(p => `${p.package} ${p.from} → ${p.to}`).join(', ')
  const dependencies = files.filter(f => f.class === 'dependency').map(f => f.path)
  const other = files.length - dependencies.length
  return [dependencies.join(', '), other ? `${other} other ${other === 1 ? 'file' : 'files'}` : ''].filter(Boolean).join(' · ')
}

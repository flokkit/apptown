import { describe, expect, it } from 'vitest'
import { parseUnifiedDiff } from './DiffView'

const modified = `diff --git a/a.txt b/a.txt
index 1111111..2222222 100644
--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,3 @@
 keep
-old
+new
 keep too
`

const renamed = `diff --git a/old.txt b/new.txt
similarity index 90%
rename from old.txt
rename to new.txt
index 1111111..2222222 100644
--- a/old.txt
+++ b/new.txt
@@ -1,2 +1,2 @@
 keep
-old
+new
`

const deleted = `diff --git a/gone.txt b/gone.txt
deleted file mode 100644
index 1111111..0000000
--- a/gone.txt
+++ /dev/null
@@ -1 +0,0 @@
-bye
`

const added = `diff --git a/fresh.txt b/fresh.txt
new file mode 100644
index 0000000..1111111
--- /dev/null
+++ b/fresh.txt
@@ -0,0 +1 @@
+hello
`

const binary = `diff --git a/img.png b/img.png
index 1111111..2222222 100644
Binary files a/img.png and b/img.png differ
`

const noNewline = `diff --git a/a.txt b/a.txt
--- a/a.txt
+++ b/a.txt
@@ -1 +1 @@
-x
\\ No newline at end of file
+y
\\ No newline at end of file
`

describe('parseUnifiedDiff', () => {
  it('parses a modified file with line numbers and counts', () => {
    const [f] = parseUnifiedDiff(modified)
    expect(f.path).toBe('a.txt')
    expect(f.status).toBe('modified')
    expect(f.adds).toBe(1)
    expect(f.dels).toBe(1)
    expect(f.lines.map((l) => l.type)).toEqual(['hunk', 'ctx', 'del', 'add', 'ctx'])
    expect(f.lines[1]).toMatchObject({ old: 1, new: 1, text: 'keep' })
    expect(f.lines[2]).toMatchObject({ old: 2, text: 'old' })
    expect(f.lines[3]).toMatchObject({ new: 2, text: 'new' })
  })

  it('reads a rename as old → new', () => {
    const [f] = parseUnifiedDiff(renamed)
    expect(f.status).toBe('renamed')
    expect(f.oldPath).toBe('old.txt')
    expect(f.path).toBe('new.txt')
  })

  it('reads a deletion, keeping the old path', () => {
    const [f] = parseUnifiedDiff(deleted)
    expect(f.status).toBe('deleted')
    expect(f.path).toBe('gone.txt')
    expect(f.dels).toBe(1)
    expect(f.adds).toBe(0)
  })

  it('reads an added file', () => {
    const [f] = parseUnifiedDiff(added)
    expect(f.status).toBe('added')
    expect(f.path).toBe('fresh.txt')
    expect(f.adds).toBe(1)
  })

  it('reads a binary file with no lines', () => {
    const [f] = parseUnifiedDiff(binary)
    expect(f.status).toBe('binary')
    expect(f.path).toBe('img.png')
    expect(f.lines).toEqual([])
  })

  it('keeps the "\\ No newline at end of file" marker as context without numbers', () => {
    const [f] = parseUnifiedDiff(noNewline)
    const markers = f.lines.filter((l) => l.text.startsWith('\\ No newline'))
    expect(markers).toHaveLength(2)
    for (const m of markers) {
      expect(m.type).toBe('ctx')
      expect(m.old).toBeUndefined()
      expect(m.new).toBeUndefined()
    }
    expect(f.adds).toBe(1)
    expect(f.dels).toBe(1)
  })

  it('splits a multi-file diff and ignores an empty one', () => {
    expect(parseUnifiedDiff(modified + renamed + binary)).toHaveLength(3)
    expect(parseUnifiedDiff('')).toEqual([])
  })
})

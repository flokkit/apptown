// A picture pasted into a chat travels in the person's message as a path
// the helper can look at ("(I pasted a picture — look at
// /console/uploads/picture-N.png)"). Pages show the pictures themselves and
// the words without that plumbing sentence.
export function pastedPictures(text: string): { display: string; names: string[]; files: { name: string; size: number }[] } {
  const names: string[] = []
  const files: { name: string; size: number }[] = []
  text = text.replace(/(?:^|\s)\S+ attached ([a-zA-Z0-9_-][a-zA-Z0-9._-]*) \((\d+) bytes\): \/console\/uploads\/\1( \(picture\))?/g, (_match, name: string, size: string, picture: string) => {
    if (picture) names.push(name)
    else files.push({ name, size: Number(size) })
    return ''
  }).trim()
  names.push(...Array.from(text.matchAll(/\/console\/uploads\/(picture-\d+\.(?:png|jpg|webp|gif))/g)).map((x) => x[1]))
  const display = names.length > 0 ? text.replace(/\s*\(I pasted[^)]*\)\s*$/, '').trim() : text
  return { display, names: Array.from(new Set(names)), files }
}

// The text roles (styles/typography.css), for the specimen page at
// /text-roles. Every class here is declared in typography.css, or in
// text.css for Small and Muted; TextRoles.test.tsx holds the two together.
export type TextRole = { role: string; className: string; elements: string; tokens: string[]; use: string; sample: string }

export const TEXT_ROLES: TextRole[] = [
  { role: 'Page title', className: 'text-title', elements: 'h1', tokens: ['--font-heading', '--fs-xl', '--fw-strong', '--lh-tight', '--ls-title', '--text'], use: "the page's name", sample: 'Club newsletter' },
  { role: 'Section heading', className: 'text-heading', elements: 'h2', tokens: ['--font-heading', '--fs-lg', '--fw-strong', '--lh-tight', '--text'], use: 'a section or dialog title', sample: 'Deployments' },
  { role: 'Subheading', className: 'text-subheading', elements: 'h3, h4, h5, h6', tokens: ['--font-heading', '--fs-base', '--fw-strong', '--text'], use: 'a heading inside a section or card', sample: 'Test inputs' },
  { role: 'Name', className: 'text-name', elements: 'strong, b', tokens: ['--fw-strong'], use: "a record's name in a row, and emphasis", sample: 'built-in-docker' },
  { role: 'Body', className: 'text-body', elements: 'body; family and weight of button, input, select', tokens: ['--font-sans', '--fs-base', '--fw-regular', '--lh-body', '--text'], use: 'running text and controls', sample: 'The newsletter is live on Production.' },
  { role: 'Label', className: 'text-label', elements: 'th, dt', tokens: ['--font-sans', '--fs-sm', '--fw-regular', '--muted'], use: "a column head, a fact's name", sample: 'Last deploy' },
  { role: 'Group label', className: 'group-label', elements: '', tokens: ['--font-sans', '--fs-xs', '--fw-strong', '--ls-caps', '--muted'], use: 'the label over a group of rows', sample: 'Household' },
  { role: 'Small', className: 'small', elements: '', tokens: ['--fs-xs'], use: 'meta lines, hints', sample: 'checked 3 minutes ago' },
  { role: 'Muted', className: 'muted', elements: '', tokens: ['--muted'], use: 'secondary words; regular inside a heading or name', sample: 'role on this project' },
  { role: 'Code', className: 'text-code', elements: 'code, pre, kbd, samp; family of textarea', tokens: ['--font-mono', '--fs-xs'], use: 'commands, keys, source, paths', sample: 'apptown deploy --env production' },
]

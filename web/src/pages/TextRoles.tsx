import { Link } from 'react-router-dom'
import { ListTable } from '../ListTable'
import { ListRow } from '../ListRow'
import { Group, Section } from '../Section'
import { TEXT_ROLES } from '../textRoles'

// Every text role, every element mapped to one (h1 is the page's own title:
// a page has one), and the roles in the lists
// and headings that use them. Not linked from navigation: the UI camera
// captures it at every width, text size and theme, so a change to a role
// shows here as well as on the pages it touches.
export default function TextRoles() {
  return <>
    <div className="page-head"><div className="page-head-row"><h1>Text roles</h1></div></div>
    <Section title="Roles" count={TEXT_ROLES.length}>
      <ListTable label="Text roles" rows={TEXT_ROLES} rowKey={r => r.className} columns={[
        { key: 'role', label: 'Role', phone: 'name', render: r => <strong>{r.role}</strong> },
        { key: 'sample', label: 'Sample', render: r => <span className={r.className}>{r.sample}</span> },
        { key: 'class', label: 'Class', className: 'technical', render: r => <code>.{r.className}</code> },
        { key: 'elements', label: 'Elements', render: r => r.elements },
        { key: 'tokens', label: 'Tokens', className: 'technical', render: r => <code>{r.tokens.join(' ')}</code> },
        { key: 'use', label: 'Used for', render: r => r.use },
      ]} />
    </Section>
    <Section title="Elements">
      <>
        <h2>Heading 2</h2><h3>Heading 3</h3><h4>Heading 4</h4><h5>Heading 5</h5><h6>Heading 6</h6>
        <p>Body text with <strong>strong words</strong>, <b>bold words</b>, <code>inline code</code>, <kbd>Ctrl</kbd> <samp>sample output</samp> and <Link to="/text-roles">a link</Link>.</p>
        <pre>pre: apptown deploy --env production</pre>
        <table><thead><tr><th>Head</th><th>Second head</th></tr></thead><tbody><tr><td>Cell</td><td>Another cell</td></tr></tbody></table>
        <dl className="facts"><dt>Term</dt><dd>Definition</dd></dl>
        <div className="row"><button>Button</button><button className="primary">Primary</button>
          <input aria-label="Text field" defaultValue="Text field" /><select aria-label="Choice"><option>Choice</option></select></div>
        <textarea aria-label="Code field" rows={2} defaultValue="code: textarea" />
        <textarea className="prose" aria-label="Prose field" rows={2} defaultValue="A prose textarea holds sentences." />
      </>
    </Section>
    <Section title="In context" count={3}>
      <Group label="A table">
        <ListTable label="In context" rows={[{ n: 12, name: 'Weekly digest', kind: 'feature' }]} rowKey={r => String(r.n)} columns={[
          { key: 'name', label: 'Name', phone: 'name', render: r => <Link to="/text-roles"><span className="muted">#{r.n} </span>{r.name}</Link> },
          { key: 'kind', label: 'Kind', render: r => r.kind },
        ]} />
      </Group>
      <Group label="A row">
        <ListRow identity={<><strong><span className="muted">#12 </span>Weekly digest</strong><div className="muted small">Asked by sam</div></>} state="running" />
      </Group>
      <h2>Heading with <span className="muted">a muted part</span></h2>
    </Section>
  </>
}

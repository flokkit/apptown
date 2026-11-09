import {
  siGitlab, siDocker, siAnthropic, siOpenrouter, siTrivy, siClaude,
  siOllama, siVllm, siGithub, siKubernetes, siDigitalocean,
  siNpm, siPypi, siRust, siGo, siNodedotjs, siApachemaven,
} from 'simple-icons'

// Connector iconography. Brand marks come from the bundled simple-icons
// package (CSP-safe — no network fetches); providers without a mark get
// hand-drawn stroke glyphs in the same 24×24 box. Icons render on a white
// tile so brand colors read on the dark theme, Harness-style.

type Brand = { path: string; hex: string; viewBox?: string }
const BRANDS: Record<string, Brand> = {
  // OpenAI's monochrome Blossom, from https://openai.com/brand/:
  // https://images.ctfassets.net/kftzwdyauwt9/3hUGLn3ypllZ0oa01qOYVq/28e8188e6f11b84c3e876569d492734f/Blossom_Light.svg
  // OpenAI owns this mark; used only to identify its model provider.
  openai: { path: 'M249.176 323.434V298.276C249.176 296.158 249.971 294.569 251.825 293.509L302.406 264.381C309.29 260.409 317.5 258.555 325.973 258.555C357.75 258.555 377.877 283.185 377.877 309.399C377.877 311.253 377.877 313.371 377.611 315.49L325.178 284.771C322.001 282.919 318.822 282.919 315.645 284.771L249.176 323.434ZM367.283 421.415V361.301C367.283 357.592 365.694 354.945 362.516 353.092L296.048 314.43L317.763 301.982C319.617 300.925 321.206 300.925 323.058 301.982L373.639 331.112C388.205 339.586 398.003 357.592 398.003 375.069C398.003 395.195 386.087 413.733 367.283 421.412V421.415ZM233.553 368.452L211.838 355.742C209.986 354.684 209.19 353.095 209.19 350.975V292.718C209.19 264.383 230.905 242.932 260.301 242.932C271.423 242.932 281.748 246.641 290.49 253.26L238.321 283.449C235.146 285.303 233.555 287.951 233.555 291.659V368.455L233.553 368.452ZM280.292 395.462L249.176 377.985V340.913L280.292 323.436L311.407 340.913V377.985L280.292 395.462ZM300.286 475.968C289.163 475.968 278.837 472.259 270.097 465.64L322.264 435.449C325.441 433.597 327.03 430.949 327.03 427.239V350.445L349.011 363.155C350.865 364.213 351.66 365.802 351.66 367.922V426.179C351.66 454.514 329.679 475.965 300.286 475.965V475.968ZM237.525 416.915L186.944 387.785C172.378 379.31 162.582 361.305 162.582 343.827C162.582 323.436 174.763 305.164 193.563 297.485V357.861C193.563 361.571 195.154 364.217 198.33 366.071L264.535 404.467L242.82 416.915C240.967 417.972 239.377 417.972 237.525 416.915ZM234.614 460.343C204.689 460.343 182.71 437.833 182.71 410.028C182.71 407.91 182.976 405.792 183.238 403.672L235.405 433.863C238.582 435.715 241.763 435.715 244.938 433.863L311.407 395.466V420.622C311.407 422.742 310.612 424.331 308.758 425.389L258.179 454.519C251.293 458.491 243.083 460.343 234.611 460.343H234.614ZM300.286 491.854C332.329 491.854 359.073 469.082 365.167 438.892C394.825 431.211 413.892 403.406 413.892 375.073C413.892 356.535 405.948 338.529 391.648 325.552C392.972 319.991 393.766 314.43 393.766 308.87C393.766 271.003 363.048 242.666 327.562 242.666C320.413 242.666 313.528 243.723 306.644 246.109C294.725 234.457 278.307 227.042 260.301 227.042C228.258 227.042 201.513 249.815 195.42 280.004C165.761 287.685 146.694 315.49 146.694 343.824C146.694 362.362 154.638 380.368 168.938 393.344C167.613 398.906 166.819 404.467 166.819 410.027C166.819 447.894 197.538 476.231 233.024 476.231C240.172 476.231 247.058 475.173 253.943 472.788C265.859 484.441 282.278 491.854 300.286 491.854Z', hex: '000000', viewBox: '146.694 225.849 267.198 267.198' },
  gitlab: siGitlab, docker: siDocker, anthropic: siAnthropic,
  openrouter: siOpenrouter, trivy: siTrivy, claude: siClaude,
  ollama: siOllama, vllm: siVllm, github: siGithub, kubernetes: siKubernetes,
  npm: siNpm, pypi: siPypi, rust: siRust, go: siGo, node: siNodedotjs,
  maven: siApachemaven, digitalocean: siDigitalocean,
}

// Stroke glyphs for providers simple-icons lacks (or generic capabilities).
const GLYPHS: Record<string, { d: string; hex: string }> = {
  infisical: { d: 'M12 3a5 5 0 0 0-5 5v3H6a1 1 0 0 0-1 1v7a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-7a1 1 0 0 0-1-1h-1V8a5 5 0 0 0-5-5zm-3 8V8a3 3 0 1 1 6 0v3H9zm3 3.5a1.5 1.5 0 0 1 .75 2.8V19h-1.5v-1.7a1.5 1.5 0 0 1 .75-2.8z', hex: 'EFB810' },
  semgrep: { d: 'M4 5h16v2.5H4V5zm0 5.75h16v2.5H4v-2.5zM4 16.5h9v2.5H4v-2.5zm12.4 0h3.6L17 21h-3z', hex: '2ACFA6' },
  osv: { d: 'M12 2l8 3.5V12c0 4.9-3.4 8.4-8 10-4.6-1.6-8-5.1-8-10V5.5L12 2zm0 2.2L6 6.8V12c0 3.7 2.5 6.5 6 7.9 3.5-1.4 6-4.2 6-7.9V6.8l-6-2.6zm-1 4.3h2v5h-2v-5zm0 6.5h2v2h-2v-2z', hex: '4285F4' },
  gitleaks: { d: 'M14.5 3a6.5 6.5 0 0 0-6.2 8.4L3 16.7V21h4.3l1.4-1.4v-2.2h2.2l1.6-1.6a6.5 6.5 0 1 0 2-12.8zm2 3a2 2 0 1 1 0 4 2 2 0 0 1 0-4z', hex: 'E0AF68' },
  sbom: { d: 'M12 2l9 5v10l-9 5-9-5V7l9-5zm0 2.3L5.6 7.9 12 11.4l6.4-3.5L12 4.3zM5 9.6l6 3.3v6.5l-6-3.3V9.6zm14 0v6.5l-6 3.3v-6.5l6-3.3z', hex: '9ECE6A' },
  scanner: { d: 'M10.5 3a7.5 7.5 0 1 0 4.6 13.4l4.2 4.2 1.7-1.7-4.2-4.2A7.5 7.5 0 0 0 10.5 3zm0 2.4a5.1 5.1 0 1 1 0 10.2 5.1 5.1 0 0 1 0-10.2zM8 10h5v1.5H8V10z', hex: '7AA2F7' },
  model: { d: 'M12 2l1.8 5.3L19 9l-5.2 1.7L12 16l-1.8-5.3L5 9l5.2-1.7L12 2zm6.5 10l.9 2.6 2.6.9-2.6.9-.9 2.6-.9-2.6-2.6-.9 2.6-.9.9-2.6zM6 15l.7 2 2 .7-2 .7-.7 2-.7-2-2-.7 2-.7.7-2z', hex: 'BB9AF7' },
  gpu: { d: 'M7 7h10v10H7V7zm2 2v6h6V9H9zM4 4h16v16H4V4zm2 2v12h12V6H6zM10 1h1.5v2H10V1zm2.5 0H14v2h-1.5V1zM10 21h1.5v2H10v-2zm2.5 0H14v2h-1.5v-2zM1 10h2v1.5H1V10zm0 2.5h2V14H1v-1.5zM21 10h2v1.5h-2V10zm0 2.5h2V14h-2v-1.5z', hex: '73DACA' },
  deploy: { d: 'M12 2c3.5 2 5.5 5.5 5.5 9.5 0 1.2-.2 2.4-.5 3.5l2.5 3-1.5 1.5-2.7-1.8c-.9.9-2 1.7-3.3 2.3-1.3-.6-2.4-1.4-3.3-2.3L5.5 19.5 4 18l2.5-3c-.3-1.1-.5-2.3-.5-3.5C6 7.5 8.5 4 12 2zm0 5a2 2 0 1 0 0 4 2 2 0 0 0 0-4z', hex: 'F7768E' },
  terminal: { d: 'M3 4h18v16H3V4zm2 2v12h14V6H5zm2 2.5L10.5 12 7 15.5 5.9 14.4 8.3 12 5.9 9.6 7 8.5zM12 14h5v1.5h-5V14z', hex: '8B93A3' },
  errors: { d: 'M12 2L1 21h22L12 2zm0 4.3L19.5 19h-15L12 6.3zM11 10h2v5h-2v-5zm0 6h2v2h-2v-2z', hex: 'F7768E' },
  registry: { d: 'M12 3l8 3v4.5c0 .8-.1 1.6-.3 2.4l-1.8-.7c.07-.55.1-1.1.1-1.7V7.4L12 5.2 6 7.4v3.1c0 3.9 2.4 6.6 6 8.2.7-.3 1.4-.7 2-1.1l1.5 1.2c-1 .8-2.2 1.5-3.5 2-4.6-1.7-8-5.2-8-10.3V6l8-3zm-1 5h2v4.6l3 1.7-1 1.7-4-2.3V8z', hex: '7AA2F7' },
}

export function ConnIcon({ name, size = 26 }: { name: string; size?: number }) {
  const brand = BRANDS[name]
  const glyph = GLYPHS[name]
  const box = Math.round(size * 1.65)
  return (
    // Only brand marks need the white ground; drawn glyphs sit on the theme's panel.
    <span className={brand ? 'icon-tile' : 'icon-tile icon-tile-glyph'} style={{ width: box, height: box }}>
      {brand ? (
        <svg viewBox={brand.viewBox ?? "0 0 24 24"} width={size} height={size} role="img" aria-label={name}>
          <path d={brand.path} fill={`#${brand.hex}`} />
        </svg>
      ) : glyph ? (
        <svg viewBox="0 0 24 24" width={size} height={size} role="img" aria-label={name}>
          <path d={glyph.d} fill={`#${glyph.hex}`} fillRule="evenodd" />
        </svg>
      ) : (
        <span style={{ fontWeight: 700, fontSize: `calc(var(--fs-base) * ${size / 20})`, color: 'var(--muted)' }}>{(name[0] ?? '?').toUpperCase()}</span>
      )}
    </span>
  )
}

// Best icon for an existing binding: provider/name/config sniffing keeps
// this purely cosmetic — no backend field needed.
export function iconForBinding(b: { name: string; capability: string; provider: string; config: Record<string, unknown> }): string {
  const img = String(b.config.image ?? '').toLowerCase()
  const n = b.name.toLowerCase()
  if (b.capability === 'scanner') {
    for (const k of ['semgrep', 'trivy', 'gitleaks']) if (img.includes(k) || n === k) return k
    if (img.includes('osv') || n === 'osv') return 'osv'
    if (img.includes('syft') || n === 'sbom') return 'sbom'
    return 'scanner'
  }
  if (b.capability === 'model') {
    if (n === 'openai' && !b.config.baseUrl && !b.config.providerDef) return 'openai'
    for (const k of ['openrouter', 'anthropic', 'ollama', 'vllm']) if (n.includes(k)) return k
    if (b.config.baseUrl || b.config.providerDef) return 'gpu'
    return 'model'
  }
  if (b.capability === 'container-registry') return 'registry'
  if (b.capability === 'registry') {
    const f = String(b.config.format ?? '')
    const m: Record<string, string> = {
      'go-proxy': 'go', 'golang-dist': 'go', npm: 'npm', pypi: 'pypi',
      cargo: 'rust', 'rust-dist': 'rust', maven: 'maven',
      'github-releases': 'github', 'node-dist': 'node', 'apache-dist': 'maven',
    }
    return m[f] ?? 'registry'
  }
  if (b.provider === 'digitalocean' || b.capability === 'cloud-account') return 'digitalocean'
  if (b.provider === 'gitlab') return 'gitlab'
  if (b.provider === 'infisical' || b.capability === 'secrets') return 'infisical'
  if (b.capability === 'deploy') return 'deploy'
  if (b.provider === 'docker') return 'docker'
  return b.capability
}

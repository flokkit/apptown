export const janitorLevelName = (level: string) => level === 'aggressive' ? 'Including major' : level === 'routine' ? 'Patch and minor' : level === 'off' ? 'Off' : 'Not recorded'

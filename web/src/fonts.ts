// The interface fonts, bundled by the build so AppTown never fetches type
// from a CDN at runtime (an offline or locked-down AppTown looks the same).
// Each file declares its subsets with unicode-range, so a browser downloads
// only the subsets a page uses. Only the weights the tokens name are loaded:
// see --fw-* and the font stacks in styles.css.
import '@fontsource/ibm-plex-sans/400.css'
import '@fontsource/ibm-plex-sans/400-italic.css'
import '@fontsource/ibm-plex-sans/500.css'
import '@fontsource/ibm-plex-sans/600.css'
import '@fontsource/ibm-plex-sans/700.css'
import '@fontsource/ibm-plex-mono/400.css'
import '@fontsource/ibm-plex-mono/500.css'

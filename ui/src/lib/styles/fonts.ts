// Fonts are bundled from npm (@fontsource), not loaded from Google Fonts:
// a self-hosted app should work on an offline LAN and not tell a third
// party every time someone opens it. Each file carries a unicode-range, so
// browsers only download the subsets a page actually uses (latin-ext covers
// names like "Zdjęcia/Łódź.jpg").
import '@fontsource/silkscreen/latin-400.css';
import '@fontsource/silkscreen/latin-ext-400.css';
import '@fontsource/vt323/latin-400.css';
import '@fontsource/vt323/latin-ext-400.css';
import '@fontsource/jetbrains-mono/latin-400.css';
import '@fontsource/jetbrains-mono/latin-ext-400.css';
import '@fontsource/jetbrains-mono/latin-500.css';
import '@fontsource/jetbrains-mono/latin-ext-500.css';
import '@fontsource/jetbrains-mono/latin-700.css';
import '@fontsource/jetbrains-mono/latin-ext-700.css';

// Génère dist/static/css/themes/goldcream.css (thème or et crème, clair et sombre) à partir du thème d'origine d'OpenLinkHub
// (default.css, jamais modifié) et de outils-build/goldcream-overrides.css (variables, polices, retouches faites à la main).
//   node outils-build/theme-goldcream.js
//
// Transformations de default.css (ligne par ligne, uniquement dans les déclarations, jamais dans les sélecteurs):
//   - gris neutres en hexadécimal   -> var(--g0..g4)  (rampe de fonds du thème clair ou sombre)
//   - rouge, vert, bleu sombres     -> var(--danger-bg), var(--ok-bg), var(--info-bg) (et leur survol)
//   - rgba(255,255,255,A)           -> rgb(var(--ov) / A)  (filets et survols: blanc en sombre, brun en clair)
//   - rgba(0,0,0,A) (ombres)        -> rgb(var(--sh) / calc(A * var(--shk)))  (ombres adoucies en clair)
//   - rgba vert/rouge/jaune/bleu    -> rgb(var(--ok-rgb|bad-rgb|warn-rgb|info-rgb) / A)
//   - color: white                  -> var(--text-strong)
//   - dégradés à 2 couleurs         -> aplat (la charte or et crème est sans dégradé)
const fs = require('fs');
const path = require('path');
const root = path.resolve(__dirname, '..') + '/';
const src = fs.readFileSync(root + 'dist/static/css/themes/default.css', 'utf8');
const over = fs.readFileSync(root + 'outils-build/goldcream-overrides.css', 'utf8');

const stats = { gris: 0, rouge: 0, vert: 0, bleu: 0, blanc: 0, noir: 0, marque: 0, texte: 0, aplat: 0 };

function classer(hex) {
  let h = hex.replace('#', '');
  if (h.length === 3) h = h.split('').map((c) => c + c).join('');
  const r = parseInt(h.slice(0, 2), 16), g = parseInt(h.slice(2, 4), 16), b = parseInt(h.slice(4, 6), 16);
  const max = Math.max(r, g, b), min = Math.min(r, g, b);
  if (max > 0x70) return null; // couleurs vives ou claires (curseurs, texte): laissées telles quelles
  if (max - min <= 6) { // gris neutre
    const v = Math.round((r + g + b) / 3);
    stats.gris++;
    if (v <= 0x14) return 'var(--g0)';
    if (v <= 0x1b) return 'var(--g1)';
    if (v <= 0x25) return 'var(--g2)';
    if (v <= 0x31) return 'var(--g3)';
    return 'var(--g4)';
  }
  if (r > g + 12 && r > b + 12) { stats.rouge++; return r >= 0x42 ? 'var(--danger-bg-h)' : 'var(--danger-bg)'; }
  if (g > r + 10 && g >= b) { stats.vert++; return g >= 0x44 ? 'var(--ok-bg-h)' : 'var(--ok-bg)'; }
  if (b > r + 15) { stats.bleu++; return b >= 0x5a ? 'var(--info-bg-h)' : 'var(--info-bg)'; }
  return null;
}

const marques = [
  [/rgba\(\s*34\s*,\s*197\s*,\s*94\s*,\s*([\d.]+)\s*\)/g, 'ok-rgb'],
  [/rgba\(\s*239\s*,\s*68\s*,\s*68\s*,\s*([\d.]+)\s*\)/g, 'bad-rgb'],
  [/rgba\(\s*250\s*,\s*204\s*,\s*21\s*,\s*([\d.]+)\s*\)/g, 'warn-rgb'],
  [/rgba\(\s*255\s*,\s*255\s*,\s*0\s*,\s*([\d.]+)\s*\)/g, 'warn-rgb'],
  [/rgba\(\s*59\s*,\s*130\s*,\s*246\s*,\s*([\d.]+)\s*\)/g, 'info-rgb'],
];

function transformer(ligne) {
  const t = ligne.trim();
  if (/\{\s*$/.test(t) || /^[.#@\[]/.test(t) || t.startsWith('/*') || t.startsWith('}')) return ligne;
  let s = ligne;
  s = s.replace(/(^|[\s:(,])(#[0-9a-fA-F]{6}|#[0-9a-fA-F]{3})\b/g, (m, pre, hex) => {
    const v = classer(hex);
    return v ? pre + v : m;
  });
  s = s.replace(/rgba\(\s*255\s*,\s*255\s*,\s*255\s*,\s*([\d.]+)\s*\)/g, (m, a) => { stats.blanc++; return `rgb(var(--ov) / ${a})`; });
  s = s.replace(/rgba\(\s*0\s*,\s*0\s*,\s*0\s*,\s*([\d.]+)\s*\)/g, (m, a) => { stats.noir++; return `rgb(var(--sh) / calc(${a} * var(--shk)))`; });
  for (const [re, nom] of marques) s = s.replace(re, (m, a) => { stats.marque++; return `rgb(var(--${nom}) / ${a})`; });
  s = s.replace(/(^|[;{\s])color\s*:\s*(white|#fff|#ffffff)\s*(!important)?\s*;/gi, (m, pre, v, imp) => { stats.texte++; return `${pre}color: var(--text-strong)${imp ? ' !important' : ''};`; });
  return s;
}

let css = src.split('\n').map(transformer).join('\n');

// Dégradés à deux couleurs (une seule ligne ou sur plusieurs lignes) -> aplat de la première couleur.
css = css.replace(/(linear-gradient)\(\s*(?:180deg|to bottom|90deg|135deg)\s*,\s*([^,()]+(?:\([^()]*\))?[^,()]*?)\s*,\s*[^,()]+(?:\([^()]*\))?[^,()]*?\s*\)/g, (m, f, a) => {
  stats.aplat++;
  return a.trim();
});

const entete = `/* Thème or et crème pour OpenLinkHub (clair par défaut, sombre au choix) — GÉNÉRÉ par outils-build/theme-goldcream.js
   à partir de default.css (non modifié) et de outils-build/goldcream-overrides.css. Ne pas modifier à la main. */\n`;
fs.writeFileSync(root + 'dist/static/css/themes/goldcream.css', entete + css + '\n/* ===== Variables et retouches du thème ===== */\n' + over);
console.log('goldcream.css écrit:', JSON.stringify(stats));

// Variante d'essai (hors dépôt, ignorée par git): polices servies par le petit serveur local http://localhost:8099
// (outils-build/serveur-essai.js), pour juger le thème sur les vraies pages du NAS sans rien y copier.
const essai = (entete + css + '\n/* ===== Variables et retouches du thème ===== */\n' + over).split("url('/static/").join("url('http://localhost:8099/static/");
fs.writeFileSync(root + 'dist/static/css/themes/goldcream.test.css', essai);

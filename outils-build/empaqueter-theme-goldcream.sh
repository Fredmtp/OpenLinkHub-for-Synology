#!/bin/bash
# Régénère le thème et construit dist/theme-goldcream.tar.gz (à copier sur le NAS avec installer-theme-goldcream.sh).
#   bash outils-build/empaqueter-theme-goldcream.sh
set -e
cd "$(dirname "$0")/.."
node outils-build/theme-goldcream.js
# inscrit la version (fichier VERSION) dans le menu d'OpenLinkHub
VERSION=$(tr -d '[:space:]' < VERSION)
sed -i "s|(id=\"goldcreamVersion\">)[^<]*|${VERSION}|" dist/web/sidebar.html
echo "version inscrite: ${VERSION}"
cp dist/retour-theme-goldcream.sh dist/retour-theme-goldcream.sh.tmp && rm dist/retour-theme-goldcream.sh.tmp
tar czf dist/theme-goldcream.tar.gz -C dist \
  static/css/themes/goldcream.css static/fonts/goldcream static/js/goldcream-theme.js static/js/nas-tiles.js static/nas-graphiques.html \
  web/head.html web/sidebar.html web/temperature-bar.html web/index.html retour-theme-goldcream.sh
tar tzf dist/theme-goldcream.tar.gz | grep -v '/$'
sha256sum dist/theme-goldcream.tar.gz dist/installer-theme-goldcream.sh

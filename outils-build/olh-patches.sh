#!/bin/bash
# Extrait nos modifications d'OpenLinkHub (dépôt ~/OpenLinkHub) sous forme de correctifs.
cd ~/OpenLinkHub || exit 1
echo "== version de base"
git log -1 --format='%H %ad %s' --date=short
git describe --always --dirty 2>/dev/null
echo "== fichiers modifiés ou nouveaux"
git status --short | grep -v '^??.*\.log$'
echo "== diffstat"
git diff --stat
mkdir -p ~/olh-patches
git diff > ~/olh-patches/modifications-suivies.patch
# fichiers nouveaux (non suivis), hors binaires et journaux
for f in $(git ls-files --others --exclude-standard | grep -E '\.(go|sh|md|txt)$'); do
  echo "nouveau: $f"
  mkdir -p ~/olh-patches/nouveaux/$(dirname "$f")
  cp "$f" ~/olh-patches/nouveaux/"$f"
done
ls -la ~/olh-patches
wc -l ~/olh-patches/modifications-suivies.patch

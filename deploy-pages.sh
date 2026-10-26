#!/bin/sh
# Builds the app and publishes web/ to GitHub Pages.
# First run creates a public repo (default name: whatsapp-browser) under your GitHub account.
set -e
cd "$(dirname "$0")"
REPO=${REPO:-whatsapp-browser}
OWNER=$(gh api user --jq .login)
NAME=$(git config user.name || echo "deploy")
EMAIL=$(git config user.email || echo "deploy@localhost")

./build.sh

if ! gh repo view "$OWNER/$REPO" >/dev/null 2>&1; then
  gh repo create "$REPO" --public --description "WhatsApp client running entirely in the browser (whatsmeow compiled to WebAssembly)"
fi
git remote get-url origin >/dev/null 2>&1 || git remote add origin "https://github.com/$OWNER/$REPO.git"
git push -u origin main

TMP=$(mktemp -d)
cp web/* "$TMP"/
touch "$TMP/.nojekyll"
(
  cd "$TMP"
  git init -q -b gh-pages
  git add -A
  git -c user.name="$NAME" -c user.email="$EMAIL" commit -q -m "Deploy $(date -u +%Y-%m-%dT%H:%MZ)"
  git push -q -f "https://github.com/$OWNER/$REPO.git" gh-pages
)
rm -rf "$TMP"

gh api -X POST "repos/$OWNER/$REPO/pages" -f "source[branch]=gh-pages" -f "source[path]=/" >/dev/null 2>&1 \
  || gh api -X PUT "repos/$OWNER/$REPO/pages" -f "source[branch]=gh-pages" -f "source[path]=/" >/dev/null

echo "Deployed. Pages usually goes live within a minute at:"
echo "https://$OWNER.github.io/$REPO/"

#!/bin/sh
# Installs CardHub's GUI view (the cardhub Herdr mod) into Herdr. Copied, not symlinked: Herdr
# loads a symlinked mod's code but not its icons. Run again after changing anything in herdr/cardhub/.
set -e
src="$(cd "$(dirname "$0")/cardhub" && pwd)"
dst="${HERDR_APP_HOME:-$HOME/.config/herdr-app}/mods/cardhub"
mkdir -p "$dst"
cp "$src"/main.js "$src"/mod.json "$src"/*.svg "$dst"/
herdr-app check "$dst"

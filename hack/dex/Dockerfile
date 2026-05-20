# if you bumping this, make sure web page is not regressed.
FROM ghcr.io/dexidp/dex:v2.42.0

ENV DEX_FRONTEND_DIR=/srv/dex/web

COPY --chown=root:root web /srv/dex/web

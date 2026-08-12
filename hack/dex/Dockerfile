# Keep this aligned with the Dex runtime used by Tiltfile.cluster. The
# frontend overlay is intentionally built on the exact upstream release so
# template fields and the image entrypoint cannot drift independently.
FROM ghcr.io/dexidp/dex:v2.44.0

ENV DEX_FRONTEND_DIR=/srv/dex/web

COPY --chown=root:root web /srv/dex/web
# Dex serves the overlay as an unprivileged user (UID 1001 in the upstream
# image). Static assets are immutable at runtime but must be traversable and
# readable by that user after Docker copies any restrictive source modes.
USER root
RUN chmod -R a+rX /srv/dex/web
USER 1001

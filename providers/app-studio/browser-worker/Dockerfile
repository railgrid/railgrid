FROM node:22-bookworm-slim AS build

WORKDIR /src
COPY package.json package-lock.json tsconfig.json ./
RUN npm ci
COPY src ./src
RUN npm run build && npm prune --omit=dev

FROM mcr.microsoft.com/playwright:v1.62.1-noble

ENV NODE_ENV=production \
    PORT=8090
WORKDIR /app
COPY --from=build /src/package.json /src/package-lock.json ./
COPY --from=build /src/node_modules ./node_modules
COPY --from=build /src/dist ./dist

USER pwuser
EXPOSE 8090
CMD ["node", "dist/server.js"]

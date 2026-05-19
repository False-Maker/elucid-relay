FROM node:22-alpine AS build

WORKDIR /app

COPY package.json package-lock.json tsconfig.json ./
COPY packages/contracts/package.json packages/contracts/tsconfig.json packages/contracts/
COPY packages/contracts/src packages/contracts/src
COPY apps/portal/package.json apps/portal/tsconfig.json apps/portal/vite.config.ts apps/portal/index.html apps/portal/
COPY apps/portal/src apps/portal/src

RUN npm ci

ARG VITE_API_BASE_URL=http://localhost:18080
ENV VITE_API_BASE_URL=$VITE_API_BASE_URL
ARG VITE_RELAY_API_BASE_URL=http://localhost:18080
ENV VITE_RELAY_API_BASE_URL=$VITE_RELAY_API_BASE_URL

RUN npm run build --workspace @elucid-relay/contracts \
  && npm run build --workspace @elucid-relay/portal

FROM nginx:1.27-alpine

COPY apps/portal/nginx.conf /etc/nginx/templates/default.conf.template
COPY --from=build /app/apps/portal/dist /usr/share/nginx/html

EXPOSE 80

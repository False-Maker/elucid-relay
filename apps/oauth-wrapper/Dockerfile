FROM node:22-alpine

WORKDIR /app/apps/oauth-wrapper

COPY apps/oauth-wrapper/package.json ./
COPY apps/oauth-wrapper/src ./src

USER node

ENTRYPOINT ["node", "src/index.mjs"]
CMD ["run"]

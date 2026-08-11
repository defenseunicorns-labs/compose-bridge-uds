FROM node:22-alpine AS build

WORKDIR /app

COPY package*.json ./
RUN npm ci

COPY . .
RUN npm run build

FROM ghcr.io/nginx/nginx-unprivileged:1.30.4-alpine3.24

COPY nginx/default.conf /etc/nginx/conf.d/default.conf
COPY nginx/api-auth.conf /etc/nginx/conf.d/includes/api-auth.conf
COPY --from=build /app/dist /usr/share/nginx/html

EXPOSE 8080

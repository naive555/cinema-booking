FROM node:20-alpine AS build
WORKDIR /app

COPY package*.json ./

RUN npm ci

COPY . .

# VITE_DEV_AUTH is a build-time flag: "true" shows the dev-login form.
# Passed via docker-compose build args; absent in production builds.
ARG VITE_DEV_AUTH=false

RUN VITE_DEV_AUTH=${VITE_DEV_AUTH} npm run build

FROM nginx:alpine

COPY --from=build /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf

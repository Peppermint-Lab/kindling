# Production image for Kindling: static SPA on port 3000 (guest agent looks for /app/package.json + npm start).
# Create a Kindling project with RootDirectory `/web/landing` and default DockerfilePath.
FROM node:22-alpine AS build
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM node:22-alpine
WORKDIR /app
ENV NODE_ENV=production
COPY package*.json ./
RUN npm ci --omit=dev
COPY --from=build /app/dist ./dist
EXPOSE 3000
CMD ["npm", "start"]

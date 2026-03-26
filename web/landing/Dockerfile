# Production image for Kindling: static SPA on port 3000 (guest agent + health checks).
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
RUN npm install -g serve@14
COPY --from=build /app/dist ./dist
EXPOSE 3000
# bind all interfaces so the runtime can reach the app
CMD ["serve", "-s", "dist", "-l", "tcp://0.0.0.0:3000", "--no-clipboard"]

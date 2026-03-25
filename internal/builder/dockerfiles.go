package builder

// Framework Dockerfile templates.
// Injected when no Dockerfile is found and a framework is detected.

var dockerfiles = map[string]string{
	"nuxt": `FROM node:22-alpine AS build
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM node:22-alpine
WORKDIR /app
COPY --from=build /app/.output .output
EXPOSE 3000
CMD ["node", ".output/server/index.mjs"]
`,

	"nextjs": `FROM node:22-alpine AS build
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM node:22-alpine
WORKDIR /app
COPY --from=build /app/.next/standalone ./
COPY --from=build /app/.next/static .next/static
COPY --from=build /app/public public
EXPOSE 3000
CMD ["node", "server.js"]
`,

	"rails": `FROM ruby:3.3-slim AS build
WORKDIR /app
RUN apt-get update && apt-get install -y build-essential libpq-dev
COPY Gemfile Gemfile.lock ./
RUN bundle install --without development test
COPY . .
RUN bundle exec rails assets:precompile 2>/dev/null || true

FROM ruby:3.3-slim
WORKDIR /app
RUN apt-get update && apt-get install -y libpq5 && rm -rf /var/lib/apt/lists/*
COPY --from=build /usr/local/bundle /usr/local/bundle
COPY --from=build /app .
EXPOSE 3000
CMD ["bundle", "exec", "rails", "server", "-b", "0.0.0.0", "-p", "3000"]
`,

	"laravel": `FROM php:8.3-cli AS build
WORKDIR /app
RUN apt-get update && apt-get install -y unzip
COPY --from=composer:latest /usr/bin/composer /usr/bin/composer
COPY composer.json composer.lock ./
RUN composer install --no-dev --no-scripts
COPY . .
RUN composer dump-autoload --optimize

FROM php:8.3-cli
WORKDIR /app
COPY --from=build /app .
EXPOSE 3000
CMD ["php", "artisan", "serve", "--host=0.0.0.0", "--port=3000"]
`,

	"go": `FROM golang:1.24-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /app/main .

FROM alpine:3.21
WORKDIR /app
COPY --from=build /app/main .
EXPOSE 3000
CMD ["/app/main"]
`,

	"node": `FROM node:22-alpine
WORKDIR /app
COPY package*.json ./
RUN npm ci --production
COPY . .
EXPOSE 3000
CMD ["npm", "start"]
`,
}

// GetDockerfile returns the Dockerfile template for a framework, or empty string.
func GetDockerfile(framework string) string {
	return dockerfiles[framework]
}

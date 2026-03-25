// Package builder handles build orchestration. Each build runs in an isolated
// dind (Docker-in-Docker) microVM. Source is downloaded as a GitHub tarball,
// framework is auto-detected if no Dockerfile is present, and the image is
// built with docker buildx and pushed to a registry.
package builder

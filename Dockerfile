FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cloudnvr-cloud ./cmd/cloud \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cloudnvr-agent ./cmd/agent

FROM alpine:3.21 AS cloud
RUN apk add --no-cache ca-certificates ffmpeg tzdata \
	&& adduser -D -H -u 10001 cloudnvr \
	&& mkdir -p /var/lib/cloudnvr/recordings /var/lib/cloudnvr-state \
	&& chown -R cloudnvr /var/lib/cloudnvr /var/lib/cloudnvr-state
COPY --from=build /out/cloudnvr-cloud /usr/local/bin/cloudnvr-cloud
USER cloudnvr
VOLUME ["/var/lib/cloudnvr/recordings"]
EXPOSE 8080
ENTRYPOINT ["cloudnvr-cloud"]

FROM alpine:3.21 AS agent
RUN apk add --no-cache ca-certificates ffmpeg tzdata \
    && adduser -D -H -u 10001 cloudnvr && mkdir -p /var/lib/cloudnvr && chown cloudnvr /var/lib/cloudnvr
COPY --from=build /out/cloudnvr-agent /usr/local/bin/cloudnvr-agent
USER cloudnvr
VOLUME ["/var/lib/cloudnvr"]
ENTRYPOINT ["cloudnvr-agent"]

FROM node:22-bookworm-slim AS web-deps
WORKDIR /app
COPY web/package.json web/package-lock.json ./
RUN npm ci

FROM web-deps AS web-build
COPY web/ ./
RUN npm run build

FROM node:22-bookworm-slim AS web
WORKDIR /app
ENV NODE_ENV=production
COPY --from=web-build /app /app
EXPOSE 3000
ENTRYPOINT ["npm", "run", "start"]

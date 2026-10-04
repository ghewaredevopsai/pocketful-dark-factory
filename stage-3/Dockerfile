# Build: Go standard library only, so nothing is fetched beyond the base image.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
COPY app ./app
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /pocketful .

# Run: a single static binary; no network, files or services needed at run time.
FROM scratch
COPY --from=build /pocketful /pocketful
ENV PORT=8080
EXPOSE 8080
USER 65534:65534
ENTRYPOINT ["/pocketful"]

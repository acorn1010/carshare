FROM golang:1.26.3 AS builder
WORKDIR /src
COPY . .
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo \
    -tags netgo,osusergo \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/app ./cmd/carshare

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/app /app
EXPOSE 3000 9090
ENTRYPOINT ["/app"]

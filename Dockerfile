FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/gophprofile-server ./cmd/gophprofile
RUN CGO_ENABLED=0 go build -trimpath -o /out/gophprofile-worker ./cmd/worker

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /out/gophprofile-server /usr/local/bin/gophprofile-server
COPY --from=builder /out/gophprofile-worker /usr/local/bin/gophprofile-worker

EXPOSE 3200
ENTRYPOINT ["gophprofile-server"]

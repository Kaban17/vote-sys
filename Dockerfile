# Статический бинарь в scratch: образ в десятки мегабайт и холодный старт в доли
# секунды. Для системы, где реагировать во время события невозможно, скорость
# подъёма инстанса — функциональное требование, а не эстетика (architecture.md §4).

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /api
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/api"]

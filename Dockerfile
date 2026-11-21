FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/throttle ./cmd/throttle \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/demo-backend ./cmd/demo-backend

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/throttle /throttle
COPY --from=build /out/demo-backend /demo-backend
COPY configs /configs
EXPOSE 8080 9090
ENTRYPOINT ["/throttle"]
CMD ["-config", "/configs/docker.json"]

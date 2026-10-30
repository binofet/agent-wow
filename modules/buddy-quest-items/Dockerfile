FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY pkg ./pkg
COPY modules/buddy-world/api ./modules/buddy-world/api
COPY modules/buddy-actions/api ./modules/buddy-actions/api
COPY modules/buddy-quest-items ./modules/buddy-quest-items
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /buddy-quest-items ./modules/buddy-quest-items/cmd
FROM scratch
USER 65532:65532
COPY --from=build /buddy-quest-items /buddy-quest-items
ENTRYPOINT ["/buddy-quest-items"]

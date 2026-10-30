FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY pkg ./pkg
COPY modules/buddy-world/api ./modules/buddy-world/api
COPY modules/buddy-actions/api ./modules/buddy-actions/api
COPY modules/buddy-trade/api ./modules/buddy-trade/api
COPY modules/buddy-loot/api ./modules/buddy-loot/api
COPY modules/buddy-professions/api ./modules/buddy-professions/api
COPY modules/buddy-inventory ./modules/buddy-inventory
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /buddy-inventory ./modules/buddy-inventory/cmd

FROM scratch
USER 65532:65532
COPY --from=build /buddy-inventory /buddy-inventory
ENTRYPOINT ["/buddy-inventory"]

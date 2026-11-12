FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY pkg ./pkg
COPY modules/buddy-world/api ./modules/buddy-world/api
COPY modules/buddy-actions/api ./modules/buddy-actions/api
COPY modules/buddy-trade ./modules/buddy-trade
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /buddy-trade ./modules/buddy-trade/cmd

FROM scratch
USER 65532:65532
COPY --from=build /buddy-trade /buddy-trade
ENTRYPOINT ["/buddy-trade"]

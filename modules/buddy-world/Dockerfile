FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY pkg ./pkg
COPY modules/buddy-world ./modules/buddy-world
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /buddy-world ./modules/buddy-world/cmd

FROM scratch
USER 65532:65532
COPY --from=build /buddy-world /buddy-world
ENTRYPOINT ["/buddy-world"]

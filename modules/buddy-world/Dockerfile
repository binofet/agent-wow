FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY pkg ./pkg
COPY modules/buddy-world ./modules/buddy-world
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /buddy-world ./modules/buddy-world/cmd

RUN mkdir /combat-events

FROM scratch
USER 65532:65532
COPY --from=build /buddy-world /buddy-world
COPY --from=build --chown=65532:65532 /combat-events /var/lib/buddy-combat-events
ENTRYPOINT ["/buddy-world"]

FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY pkg ./pkg
COPY modules/buddy-party ./modules/buddy-party
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /buddy-party ./modules/buddy-party/cmd

FROM scratch
USER 65532:65532
COPY --from=build /buddy-party /buddy-party
ENTRYPOINT ["/buddy-party"]

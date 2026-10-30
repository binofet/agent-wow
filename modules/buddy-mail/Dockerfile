FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY pkg ./pkg
COPY modules/buddy-world/api ./modules/buddy-world/api
COPY modules/buddy-actions/api ./modules/buddy-actions/api
COPY modules/buddy-mail ./modules/buddy-mail
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /buddy-mail ./modules/buddy-mail/cmd
FROM scratch
USER 65532:65532
COPY --from=build /buddy-mail /buddy-mail
ENTRYPOINT ["/buddy-mail"]

FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY pkg ./pkg
COPY modules/buddy-chat ./modules/buddy-chat
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /buddy-chat ./modules/buddy-chat/cmd

FROM scratch
USER 65532:65532
COPY --from=build /buddy-chat /buddy-chat
ENTRYPOINT ["/buddy-chat"]

FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY pkg ./pkg
COPY modules/buddy-world/api ./modules/buddy-world/api
COPY modules/buddy-actions/api ./modules/buddy-actions/api
COPY modules/buddy-quests ./modules/buddy-quests
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /buddy-quests ./modules/buddy-quests/cmd

FROM scratch
USER 65532:65532
COPY --from=build /buddy-quests /buddy-quests
ENTRYPOINT ["/buddy-quests"]

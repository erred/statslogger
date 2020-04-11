FROM golang:alpine AS build

WORKDIR /workspace
ENV CGO_ENABLED=0
RUN apk add --update ca-certificates
COPY . .
RUN go build -o server


FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /workspace/server /server

ENTRYPOINT [ "/server" ]

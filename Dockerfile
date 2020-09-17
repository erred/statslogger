FROM golang:alpine AS build

WORKDIR /workspace
ENV CGO_ENABLED=0
COPY . .
RUN go build -trimpath -o /bin/statslogger


FROM scratch

COPY --from=build /bin/statslogger /bin/

ENTRYPOINT [ "/bin/statslogger" ]

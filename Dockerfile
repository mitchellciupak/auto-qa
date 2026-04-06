FROM golang:1.26-alpine AS build

ARG VERSION

RUN apk update && apk add --no-cache git tzdata
WORKDIR /build
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-X main.Version=${VERSION}" -o auto-qa ./

FROM scratch AS final
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /build/auto-qa /auto-qa
USER 10001:10001
CMD [ "/auto-qa" ]

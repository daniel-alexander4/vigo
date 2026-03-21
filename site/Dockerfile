FROM golang:1.26-alpine AS builder

WORKDIR /build
COPY site/go.mod site/go.sum site/
COPY site/main.go site/
COPY site/static/ site/static/
COPY site/templates/ site/templates/
RUN cd site && go build -o /vigo-site .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /vigo-site /usr/local/bin/vigo-site
COPY COMMERCIAL-TERMS.md /content/

ENV VIGO_CONTENT_DIR=/content
WORKDIR /content

EXPOSE 443
CMD ["vigo-site"]

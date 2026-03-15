FROM golang:1.26-alpine AS builder

WORKDIR /build
COPY site/go.mod site/go.sum site/
COPY site/main.go site/
COPY site/static/ site/static/
COPY site/templates/ site/templates/
RUN cd site && go build -o /hobnob-site .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /hobnob-site /usr/local/bin/hobnob-site
COPY docs/ /docs/
COPY COMMERCIAL-TERMS.md /content/

ENV HOBNOB_DOCS_DIR=/docs
ENV HOBNOB_CONTENT_DIR=/content
WORKDIR /content

EXPOSE 443
CMD ["hobnob-site"]

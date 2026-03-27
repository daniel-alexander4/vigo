FROM golang:1.26-alpine AS builder

WORKDIR /build
COPY site/go.mod site/go.sum site/
COPY site/main.go site/stats.go site/
COPY site/static/ site/static/
COPY site/templates/ site/templates/
RUN cd site && go build -o /vigo-site .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /vigo-site /usr/local/bin/vigo-site

# Content files served as markdown pages.
COPY LICENSE /content/LICENSE
COPY COMMERCIAL-TERMS.md /content/COMMERCIAL-TERMS.md
COPY docs/getting-started/overview.md /content/overview/overview.md
COPY docs/getting-started/technical-comparison.md /content/overview/technical-comparison.md
COPY docs/getting-started/performance-1m.md /content/overview/performance-1m.md
COPY docs/getting-started/performance-15s.md /content/overview/performance-15s.md
COPY docs/getting-started/performance-30s.md /content/overview/performance-30s.md
COPY docs/concepts/composition-patterns.md /content/overview/composition-patterns.md
COPY docs/examples/migration/puppet.md /content/migration/puppet.md
COPY docs/examples/migration/ansible.md /content/migration/ansible.md
COPY docs/examples/migration/chef.md /content/migration/chef.md
COPY docs/examples/migration/salt.md /content/migration/salt.md
COPY docs/examples/migration/cfengine.md /content/migration/cfengine.md

ENV VIGO_CONTENT_DIR=/content
ENV VIGO_STATS_DIR=/data
WORKDIR /content

RUN mkdir -p /data

EXPOSE 8080
VOLUME ["/data"]
CMD ["vigo-site"]

# TalkIntent Hub production container for A4A deployment
# Built FROM scratch for complete isolation without external image registry dependencies.
FROM scratch

# Root CA bundle for outbound HTTPS requests (e.g. open.feishu.cn)
COPY ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Runtime non-root user (UID 10001, GID 10001)
COPY passwd /etc/passwd
COPY group /etc/group

# Precompiled static binary
COPY talkintent /talkintent

# Switch to dedicated non-root user
USER 10001:10001

EXPOSE 18800

ENTRYPOINT ["/talkintent"]
CMD ["hub", "-addr", ":18800", "-data-dir", "/data", "-public-url", "https://talkintent.empeirion.cn", "-rate-limit-qpm", "120", "-rate-limit-burst", "30"]

FROM porsche-m3-public-runtime:alpine3.20-amd64
WORKDIR /app
COPY server bootstrap-root ./
LABEL org.opencontainers.image.revision="04ed72806f5ca139d219d166452e0473ba5bf1a1"
LABEL codex.task="m3-diagnostic-candidate"
ENV APP_ENV=production
EXPOSE 8000
CMD ["./server"]

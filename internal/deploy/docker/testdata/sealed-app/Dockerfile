FROM postgres:16-alpine
COPY handler.sh /srv/handler.sh
ENTRYPOINT ["nc", "-lk", "-p", "8080", "-e", "/srv/handler.sh"]

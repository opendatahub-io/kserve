FROM quay.io/rh-ee-allausas/golang:1.22-bookworm

RUN mkdir /app && mkdir /.cache && chmod -R 777 /app && chmod -R 777 /.cache && chmod -R 777 /usr/local/bin
RUN mkdir /.vscode-server && chmod 777 /.vscode-server && mkdir /.config && chmod 777 /.config
FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive
ENV TZ=Asia/Shanghai
ENV LANG=C.UTF-8
ENV LC_ALL=C.UTF-8

RUN apt-get update && apt-get install -y --no-install-recommends \
    wget \
    ca-certificates \
    socat \
    procps \
    curl \
    iproute2 \
    coreutils \
    && rm -rf /var/lib/apt/lists/*

COPY conf/localtime /etc/localtime
RUN echo "Asia/Shanghai" > /etc/timezone

WORKDIR /app

# 根据系统架构自动拉取对应架构的官方 4.2.3.0 客户端
RUN ARCH=$(uname -m) && \
    if [ "$ARCH" = "aarch64" ] || [ "$ARCH" = "arm64" ]; then \
      DOWNLOAD_URL="https://www.91ajs.com/files/downloads/linux/ajiasu-aarch64-4.2.3.0.tar.gz"; \
    else \
      DOWNLOAD_URL="https://www.91ajs.com/files/downloads/linux/ajiasu-amd64-4.2.3.0.tar.gz"; \
    fi && \
    echo "Downloading aijiasu for $ARCH from $DOWNLOAD_URL" && \
    wget "$DOWNLOAD_URL" -O ajiasu.tar.gz && \
    tar -zxvf ajiasu.tar.gz && \
    mv ajiasu /usr/local/bin/ajiasu && \
    chmod +x /usr/local/bin/ajiasu && \
    rm ajiasu.tar.gz

COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

EXPOSE 1080
ENTRYPOINT ["/app/entrypoint.sh"]

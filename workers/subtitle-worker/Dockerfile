FROM python:3.12-slim

ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
# 同 src-tree/Dockerfile.build-and-run 的 ARG。中国区构建：
# --build-arg APT_MIRROR_URL=https://mirrors.tuna.tsinghua.edu.cn
ARG APT_MIRROR_URL=http://deb.debian.org
# 中国区构建强烈推荐：--build-arg PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple/
# faster-whisper 拉 av/torch 等大包，从 PyPI 直接下行慢得离谱（30 kB/s）。
ARG PIP_INDEX_URL=https://pypi.org/simple/

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    HTTP_PROXY=${HTTP_PROXY} \
    HTTPS_PROXY=${HTTPS_PROXY} \
    NO_PROXY=${NO_PROXY} \
    http_proxy=${HTTP_PROXY} \
    https_proxy=${HTTPS_PROXY} \
    no_proxy=${NO_PROXY}

# apt 加固 + 可选切镜像：单连接挂死 30s abort + retry 3 次，pipeline=0 串行避免一拉死全。
RUN echo 'Acquire::http::Timeout "30";\nAcquire::http::Pipeline-Depth "0";\nAcquire::Retries "3";' > /etc/apt/apt.conf.d/99retries && \
    if [ "$APT_MIRROR_URL" != "http://deb.debian.org" ]; then \
        echo "Switching apt mirror to $APT_MIRROR_URL"; \
        sed -i "s|http://deb.debian.org|$APT_MIRROR_URL|g" /etc/apt/sources.list.d/*.sources 2>/dev/null || true; \
        sed -i "s|http://deb.debian.org|$APT_MIRROR_URL|g" /etc/apt/sources.list 2>/dev/null || true; \
    fi

RUN apt-get update \
    && apt-get install -y --no-install-recommends ffmpeg fonts-noto-cjk intel-media-va-driver vainfo \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir --index-url "$PIP_INDEX_URL" -r /app/requirements.txt

COPY app.py worker_core.py ass_generator.py segmenter.py /app/

EXPOSE 8091

CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8091"]

# The image the Python sandbox runs in (PYTHON_BACKEND=gvisor). Build with:
#   docker build -t adytus-sandbox deploy/sandbox
# Packages are installed at build time; containers run under gVisor with no
# network and a read-only root, so only /work and /tmp (small tmpfs mounts)
# are writable.
FROM python:3.12.15-slim

COPY requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -r /tmp/requirements.txt && rm /tmp/requirements.txt

ENV MPLBACKEND=Agg \
    MPLCONFIGDIR=/tmp/matplotlib \
    PYTHONDONTWRITEBYTECODE=1 \
    HOME=/work
USER 65534:65534
WORKDIR /work

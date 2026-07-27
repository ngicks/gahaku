# The gahaku server image: the binary plus the LibreOffice the office pipeline
# converts with (D-C). Nothing else is a runtime dependency — the PDF raster
# runs pdfium as WebAssembly (D-A) and the image codecs are pure Go (D-05).

FROM docker.io/library/golang:1.26-bookworm AS build

WORKDIR /src

# The module graph first, so a source-only change reuses the download layer.
COPY go.mod go.sum ./
RUN go mod download

COPY api ./api
COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg

# CGO_ENABLED=0 leaves a binary with no dynamic dependency on the build image's
# libc, which is what lets the runtime stage carry only LibreOffice.
RUN CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o /out/gahaku ./cmd/gahaku

FROM docker.io/library/debian:bookworm-slim

# Debian over Alpine: LibreOffice on Alpine is a community package built
# against musl, and font coverage there is thinner than what the fonts-*
# packages below give. The -nogui variants of the three document applications
# drop the X11/GTK stack a --headless conversion never uses, which is most of
# what a full libreoffice install would cost.
#
# Fonts are not optional: a converter with none renders every glyph as a box.
# ca-certificates is what makes an https presigned url verifiable.
RUN apt-get update \
	&& apt-get install --yes --no-install-recommends \
		ca-certificates \
		fonts-dejavu-core \
		fonts-liberation \
		libreoffice-calc-nogui \
		libreoffice-impress-nogui \
		libreoffice-writer-nogui \
	&& rm -rf /var/lib/apt/lists/*

# /usr/bin/soffice comes from libreoffice-common, which the -nogui packages
# depend on rather than ship. The office pipeline looks the binary up on PATH
# and its tests skip when it is absent, so a package set that stopped pulling it
# in has to break the build here instead of every office render later.
RUN command -v soffice

# An unprivileged user with a home of its own: the per-job soffice profile goes
# to the job directory, but fontconfig still wants somewhere to cache.
RUN useradd --create-home --shell /usr/sbin/nologin gahaku
USER gahaku
ENV HOME=/home/gahaku

COPY --from=build /out/gahaku /usr/local/bin/gahaku

# The default --listen of `gahaku serve`.
EXPOSE 9000

ENTRYPOINT ["gahaku", "serve"]

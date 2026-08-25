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
# Fonts are not optional: a converter with none renders every glyph as a box,
# and a missing family is silently substituted, which reflows the page. Two
# jobs are being done here.
#
# Metric-compatible substitutes, so a document authored against a Microsoft
# font keeps its line and page breaks: liberation stands in for Arial, Times
# New Roman and Courier New, carlito for Calibri and caladea for Cambria —
# the latter two were the Office defaults from 2007 until Aptos replaced them,
# so they matter as much as liberation does. dejavu-core is what fontconfig
# falls back to when nothing else matches.
#
# Script coverage, so text outside Latin-1 has glyphs at all: noto-core carries
# Greek, Cyrillic, Arabic, Hebrew, Thai, Devanagari and the other Indic
# scripts, Armenian and Georgian (12 MB to download), and noto-cjk carries
# Japanese, Simplified and Traditional Chinese and Korean in one family (54 MB
# to download, roughly double that installed — the single largest thing in this
# image after LibreOffice itself, so drop it if the deployment is known to be
# Latin-only). Emoji are their own font because they are colour, not an outline
# any text family provides.
#
# Both stages read these. LibreOffice resolves them through fontconfig and
# embeds what it used into the PDF it exports; the pdf stage mounts /usr/share
# into the pdfium wasm sandbox (pkg/render/pdfjob) so a PDF that names a font
# without embedding it — CJK documents above all — still finds glyphs here.
#
# ca-certificates is what makes an https presigned url verifiable.
RUN apt-get update \
	&& apt-get install --yes --no-install-recommends \
		ca-certificates \
		fonts-crosextra-caladea \
		fonts-crosextra-carlito \
		fonts-dejavu-core \
		fonts-liberation \
		fonts-noto-cjk \
		fonts-noto-color-emoji \
		fonts-noto-core \
		libreoffice-calc-nogui \
		libreoffice-impress-nogui \
		libreoffice-writer-nogui \
	&& rm -rf /var/lib/apt/lists/*

# Which of Noto CJK's four regional faces a document gets is decided by the
# locale of the soffice process, not by anything in the document: the same file
# naming SimSun and MS Mincho comes out in the sc faces under LANG=zh_CN and
# the jp faces under LANG=ja_JP, and with no locale set at all it lands on sc
# or hk. Han unification is why that matters — every face carries the whole
# repertoire, so nothing goes missing either way, but a few hundred unified
# ideographs (直 骨 令 门) are drawn in the wrong regional shape.
#
# This rule fixes the answer at Japanese without setting a process locale.
# Rejecting the sibling faces is what does the work: pattern rules alone do not
# reach the fallback list LibreOffice builds for itself, and rejecting costs no
# coverage because the jp face draws simplified and traditional Chinese too.
# The aliases below it are for documents that name a Windows Japanese font,
# which is most of them, and they are what keeps mincho serif and gothic sans
# instead of collapsing both onto one face.
COPY deployment/fontconfig/59-gahaku-prefer-ja.conf /etc/fonts/conf.d/

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

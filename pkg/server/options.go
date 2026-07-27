package server

import (
	gahakuv1 "github.com/ngicks/gahaku/api/gen/proto/go/ngicks/gahaku/v1"
	"github.com/ngicks/gahaku/pkg/render"
	"github.com/ngicks/gahaku/pkg/worker"
)

// formats maps the wire enum onto the job protocol's format names.
var formats = map[gahakuv1.ImageFormat]worker.Format{
	gahakuv1.ImageFormat_IMAGE_FORMAT_PNG:  worker.FormatPng,
	gahakuv1.ImageFormat_IMAGE_FORMAT_JPEG: worker.FormatJpeg,
	gahakuv1.ImageFormat_IMAGE_FORMAT_GIF:  worker.FormatGif,
	gahakuv1.ImageFormat_IMAGE_FORMAT_TIFF: worker.FormatTiff,
	gahakuv1.ImageFormat_IMAGE_FORMAT_BMP:  worker.FormatBmp,
	gahakuv1.ImageFormat_IMAGE_FORMAT_WEBP: worker.FormatWebp,
	gahakuv1.ImageFormat_IMAGE_FORMAT_AVIF: worker.FormatAvif,
}

// renderOptions translates the request's options into the job protocol's own.
// A nil message is the empty request — every pipeline default — because the
// zero value of each option already means "whatever the pipeline picks".
func renderOptions(options *gahakuv1.RenderOptions) (worker.Options, error) {
	format, err := imageFormat(options.GetFormat())
	if err != nil {
		return worker.Options{}, err
	}
	return worker.Options{
		Format: format,
		Resize: worker.Resize{
			MaxWidth:  options.GetResize().GetMaxWidth(),
			MaxHeight: options.GetResize().GetMaxHeight(),
		},
		Dpi: options.GetDpi(),
		Pages: worker.PageRange{
			FirstPage: options.GetPages().GetFirstPage(),
			LastPage:  options.GetPages().GetLastPage(),
		},
	}, nil
}

// imageFormat resolves the requested output format, checking here rather than
// in the worker that something can encode it.
func imageFormat(f gahakuv1.ImageFormat) (worker.Format, error) {
	if f == gahakuv1.ImageFormat_IMAGE_FORMAT_UNSPECIFIED {
		return "", nil
	}
	format, ok := formats[f]
	if !ok || !render.CanEncode(format) {
		return "", &render.UnsupportedFormatError{Format: worker.Format(f.String())}
	}
	return format, nil
}

// resolveRange closes the open first page of a requested range and checks its
// ordering, which is as far as a range can be resolved without the document.
// A last page of 0 stays open: only the document says where it ends.
func resolveRange(pages worker.PageRange) (first, last uint32, err error) {
	first = pages.FirstPage
	if first == 0 {
		first = 1
	}
	last = pages.LastPage
	if last != 0 && last < first {
		return 0, 0, &render.InvalidPageRangeError{FirstPage: int(first), LastPage: int(last)}
	}
	return first, last, nil
}

// pageCount is the length of a resolved range, 0 when it runs to the end of a
// document whose length is not known yet.
func pageCount(first, last uint32) int {
	if last == 0 {
		return 0
	}
	return int(last - first + 1)
}

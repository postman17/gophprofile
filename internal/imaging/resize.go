package imaging

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"net/http"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	MimeJPEG = "image/jpeg"
	MimePNG  = "image/png"
	MimeWebP = "image/webp"

	jpegQuality = 85
)

var (
	ErrUnsupportedFormat = errors.New("unsupported format")
	ErrInvalidImage      = errors.New("invalid image data")
)

type Size struct {
	Name   string
	Width  int
	Height int
}

var DefaultSizes = []Size{
	{Name: "100x100", Width: 100, Height: 100},
	{Name: "300x300", Width: 300, Height: 300},
}

type Thumbnail struct {
	Size     Size
	Data     []byte
	MimeType string
}

func SniffMime(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	detected := http.DetectContentType(head)
	if idx := len(detected); idx > 0 {
		if detected == "application/octet-stream" {
			return ""
		}
	}
	return detected
}

func SupportedMime(mime string) bool {
	switch mime {
	case MimeJPEG, MimePNG, MimeWebP:
		return true
	default:
		return false
	}
}

func Extension(mime string) string {
	switch mime {
	case MimeJPEG:
		return ".jpg"
	case MimePNG:
		return ".png"
	case MimeWebP:
		return ".webp"
	default:
		return ".bin"
	}
}

func Decode(data []byte) (image.Image, string, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %s", ErrInvalidImage, err)
	}
	return img, SniffMime(data), nil
}

func Dimensions(img image.Image) (int, int) {
	bounds := img.Bounds()
	return bounds.Dx(), bounds.Dy()
}

func Resize(src image.Image, width, height int) image.Image {
	if width <= 0 || height <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 0, 0))
	}

	bounds := src.Bounds()
	srcWidth, srcHeight := float64(bounds.Dx()), float64(bounds.Dy())
	if srcWidth <= 0 || srcHeight <= 0 {
		return image.NewRGBA(image.Rect(0, 0, width, height))
	}

	scale := math.Max(float64(width)/srcWidth, float64(height)/srcHeight)
	scaledWidth := int(math.Round(srcWidth * scale))
	scaledHeight := int(math.Round(srcHeight * scale))
	if scaledWidth < width {
		scaledWidth = width
	}
	if scaledHeight < height {
		scaledHeight = height
	}

	scaled := image.NewRGBA(image.Rect(0, 0, scaledWidth, scaledHeight))
	draw.CatmullRom.Scale(scaled, scaled.Bounds(), src, bounds, draw.Src, nil)

	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	offset := image.Pt((scaledWidth-width)/2, (scaledHeight-height)/2)
	draw.Draw(dst, dst.Bounds(), scaled, offset, draw.Src)
	return dst
}

func Encode(img image.Image, format string) ([]byte, string, error) {
	var buf bytes.Buffer
	switch format {
	case "jpeg", "jpg":
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
			return nil, "", fmt.Errorf("encode jpeg: %w", err)
		}
		return buf.Bytes(), MimeJPEG, nil
	case "png":
		if err := png.Encode(&buf, img); err != nil {
			return nil, "", fmt.Errorf("encode png: %w", err)
		}
		return buf.Bytes(), MimePNG, nil
	default:
		return nil, "", fmt.Errorf("%w: %s", ErrUnsupportedFormat, format)
	}
}

func Thumbnails(img image.Image, sizes []Size) ([]Thumbnail, error) {
	thumbs := make([]Thumbnail, 0, len(sizes))
	for _, size := range sizes {
		resized := Resize(img, size.Width, size.Height)
		data, mime, err := Encode(resized, "jpeg")
		if err != nil {
			return nil, err
		}
		thumbs = append(thumbs, Thumbnail{Size: size, Data: data, MimeType: mime})
	}
	return thumbs, nil
}
